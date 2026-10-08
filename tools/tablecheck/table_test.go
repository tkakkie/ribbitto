package tablecheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	pgquery "github.com/pganalyze/pg_query_go/v6"

	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// Read the literal returned by moduleManifest, not a second ownership registry.
// Reject computed manifests rather than silently checking a different value.
func ownership(source string) (map[string]string, map[string]bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "manifest.go", source, 0)
	if err != nil {
		return nil, nil, err
	}
	owners, modules := map[string]string{}, map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "moduleManifest" {
			continue
		}
		if fn.Recv != nil || fn.Type.Params.NumFields() != 0 || fn.Body == nil || len(fn.Body.List) != 1 {
			break
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			break
		}
		list, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			break
		}
		for _, item := range list.Elts {
			m, ok := item.(*ast.CompositeLit)
			if !ok {
				return nil, nil, fmt.Errorf("nonliteral module")
			}
			name, tables, fields := "", []string{}, map[string]bool{}
			for _, field := range m.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					return nil, nil, fmt.Errorf("unkeyed module")
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || fields[key.Name] {
					return nil, nil, fmt.Errorf("invalid module field")
				}
				fields[key.Name] = true
				values := []ast.Expr{kv.Value}
				if key.Name == "ownsTables" {
					v, ok := kv.Value.(*ast.CompositeLit)
					if !ok {
						return nil, nil, fmt.Errorf("nonliteral ownsTables")
					}
					values = v.Elts
				} else if key.Name != "root" {
					continue
				}
				for _, value := range values {
					v, ok := value.(*ast.BasicLit)
					if !ok || v.Kind != token.STRING {
						return nil, nil, fmt.Errorf("nonliteral ownership value")
					}
					s, err := strconv.Unquote(v.Value)
					if err != nil {
						return nil, nil, err
					}
					if key.Name == "root" {
						if !strings.HasPrefix(s, "internal/") {
							return nil, nil, fmt.Errorf("module root must start with internal/")
						}
						name = strings.TrimPrefix(s, "internal/")
					} else {
						tables = append(tables, s)
					}
				}
			}
			if name == "" || strings.Contains(name, "/") || modules[name] || !fields["ownsTables"] {
				return nil, nil, fmt.Errorf("missing or duplicate module ownership: %q", name)
			}
			modules[name] = true
			for _, table := range tables {
				if table == "" || owners[table] != "" {
					return nil, nil, fmt.Errorf("missing or duplicate table: %q", table)
				}
				owners[table] = name
			}
		}
		if len(modules) > 0 {
			return owners, modules, nil
		}
	}
	return nil, nil, fmt.Errorf("moduleManifest must return a literal module table")
}

type exemption struct{ query, table, reason string }

func checkSQL(sql, module, query string, owners map[string]string, allow map[exemption]bool) error {
	tree, err := sqlwalk.Parse(sql)
	if err != nil {
		return err
	}
	statements := sqlwalk.Statements(tree)
	if len(statements) != 1 {
		return fmt.Errorf("expected one statement")
	}
	bigints, err := sqlwalk.BigintLocations(sql)
	if err != nil {
		return err
	}
	access := func(table string, write bool) error {
		owner := owners[table]
		if owner == "" {
			return fmt.Errorf("unknown table %s", table)
		}
		if owner == module {
			return nil
		}
		if !write {
			for e := range allow {
				if e.query == query && e.table == table {
					allow[e] = true
					return nil
				}
			}
		}
		return fmt.Errorf("foreign table %s (write=%v, owner=%s)", table, write, owner)
	}
	return sqlwalk.Walk(sqlwalk.Object(statements[0])["stmt"], sqlwalk.Scope{}, sqlwalk.Options{
		NodesOnly: true, LexicalCTEs: true,
		Relation: func(r map[string]any, write bool, _ sqlwalk.Scope) error {
			if r["schemaname"] != nil || r["catalogname"] != nil {
				if write {
					return fmt.Errorf("unsupported qualified write")
				}
				return fmt.Errorf("unsupported qualified read")
			}
			return access(r["relname"].(string), write)
		},
		Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
			switch tag {
			case "SelectStmt", "InsertStmt", "UpdateStmt", "DeleteStmt":
				if n["intoClause"] != nil {
					return fmt.Errorf("unsupported SELECT INTO")
				}
			case "RangeVar":
			case "FuncCall":
				parts := []string{}
				for _, part := range n["funcname"].([]any) {
					parts = append(parts, sqlwalk.Node(part, "String")["sval"].(string))
				}
				name := strings.Join(parts, ".")
				// Function bodies can hide reads and writes from this walker.
				switch {
				case len(parts) == 2 && parts[0] == "sqlc" && (parts[1] == "arg" || parts[1] == "narg"):
				case len(parts) == 1 && (name == "count" || name == "max" || name == "lower"):
				default:
					return fmt.Errorf("unsupported function %s", name)
				}
			case "LockingClause":
				if n["lockedRels"] != nil {
					return fmt.Errorf("unsupported FOR UPDATE OF (relation or alias)")
				}
			case "A_Expr":
				parts := n["name"].([]any)
				if len(parts) != 1 {
					return fmt.Errorf("unsupported qualified operator")
				}
				name := sqlwalk.Node(parts[0], "String")["sval"].(string)
				// These are the only operators used in db/queries; others can
				// invoke user-defined functions with hidden table access.
				switch name {
				case "=", "<", ">", "<=", "+":
				default:
					return fmt.Errorf("unsupported operator %s", name)
				}
			case "TypeCast":
				// Embedded type records have no node tag for the walker.
				n = n["typeName"].(map[string]any)
				fallthrough
			case "TypeName":
				parts := n["names"].([]any)
				name := sqlwalk.Node(parts[0], "String")["sval"].(string)
				// db/queries casts only to uuid, jsonb and bigint (plus uuid[]).
				// The parser rewrites BIGINT to pg_catalog.int8, so require
				// the original keyword token to distinguish explicit qualification.
				builtin := len(parts) == 1 && (name == "uuid" || name == "jsonb")
				if len(parts) == 2 && name == "pg_catalog" && sqlwalk.Node(parts[1], "String")["sval"] == "int8" && bigints[n["location"].(float64)] {
					builtin = true
				}
				if !builtin {
					return fmt.Errorf("unsupported type %s", name)
				}
			case "ResTarget", "ColumnRef", "A_Star", "A_Const", "String", "Integer", "ParamRef", "BoolExpr", "NullTest", "SubLink", "RangeSubselect", "JoinExpr", "Alias", "SortBy", "List", "CoalesceExpr", "MinMaxExpr", "OnConflictClause", "InferClause", "IndexElem":
			default:
				return fmt.Errorf("unsupported SQL node %s", tag)
			}
			return nil
		},
	})
}

// Read exemptions use module.QueryName table reason, matching scopecheck keys.
func readExemptions(text string) (map[exemption]bool, error) {
	allow := map[exemption]bool{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid read exemption: %q", line)
		}
		key := parts[0] + "/" + parts[1]
		if seen[key] {
			return nil, fmt.Errorf("duplicate read exemption: %s", key)
		}
		seen[key] = true
		allow[exemption{parts[0], parts[1], parts[2]}] = false
	}
	if err := checkExemptions(allow, false); err != nil {
		return nil, err
	}
	return allow, nil
}

func checkExemptions(allow map[exemption]bool, stale bool) error {
	seen := map[string]bool{}
	queryKey := regexp.MustCompile(`^\w+\.\w+$`)
	for e, used := range allow {
		key := e.query + "/" + e.table
		if !queryKey.MatchString(e.query) || e.table == "" || strings.TrimSpace(e.reason) == "" || (sqlwalk.ReasonRules{ReviewComments: true}).Check(e.reason) != nil || seen[key] || stale && !used {
			return fmt.Errorf("invalid, pending or stale read exemption: %s", key)
		}
		seen[key] = true
	}
	return nil
}

func TestProductionOwnership(t *testing.T) {
	source, err := os.ReadFile("../../module_imports_test.go")
	if err != nil {
		t.Fatal(err)
	}
	owners, modules, err := ownership(string(source))
	if err != nil {
		t.Fatal(err)
	}
	// Cross-module flows use injected APIs, not direct-SQL exemptions.
	allow, err := readExemptions("")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migration files: %v", err)
	}
	migrations := []string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		migrations = append(migrations, string(data))
	}
	if err := checkMigrations(migrations, owners); err != nil {
		t.Fatal(err)
	}
	queries, err := sqlwalk.Load("../../db/queries", modules)
	if err != nil {
		t.Fatal(err)
	}
	for query, sql := range queries {
		module, _, _ := strings.Cut(query, ".")
		if err := checkSQL(sql, module, query, owners, allow); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := checkExemptions(allow, true); err != nil {
		t.Fatal(err)
	}
}

func checkQueries(sql, file, module string, owners map[string]string, allow map[exemption]bool) error {
	queries, err := sqlwalk.Split(sql, module)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	for query, body := range queries {
		if err := checkSQL(body, module, query, owners, allow); err != nil {
			return fmt.Errorf("%s: %w", query, err)
		}
	}
	return nil
}

func checkMigrations(migrations []string, owners map[string]string) error {
	tables := map[string]bool{}
	for _, sql := range migrations {
		_, up, ok := strings.Cut(sql, "-- +goose Up")
		if !ok {
			return fmt.Errorf("missing migration Up section")
		}
		up, _, _ = strings.Cut(up, "-- +goose Down")
		tree, err := pgquery.Parse(up)
		if err != nil {
			return err
		}
		for _, raw := range tree.Stmts {
			n := raw.Stmt
			if n.GetDoStmt() != nil {
				return fmt.Errorf("unsupported migration DO block")
			}
			if fn := n.GetCreateFunctionStmt(); fn != nil {
				for _, option := range fn.Options {
					if def := option.GetDefElem(); def.GetDefname() == "as" {
						for _, body := range def.Arg.GetList().GetItems() {
							if err := checkMigrationBody(body.GetString_().GetSval()); err != nil {
								return err
							}
						}
					}
				}
				if fn.SqlBody != nil {
					end := len(up)
					if raw.StmtLen != 0 {
						end = int(raw.StmtLocation + raw.StmtLen)
					}
					if err := checkMigrationBody(up[raw.StmtLocation:end]); err != nil {
						return err
					}
				}
			}
			if n.GetDropStmt().GetRemoveType() == pgquery.ObjectType_OBJECT_TABLE || n.GetRenameStmt().GetRenameType() == pgquery.ObjectType_OBJECT_TABLE || n.GetCreateTableAsStmt() != nil {
				return fmt.Errorf("unsupported migration table drop, rename or CREATE AS")
			}
			if create := n.GetCreateStmt(); create != nil {
				r := create.Relation
				if r.Schemaname != "" || r.Catalogname != "" || tables[r.Relname] {
					return fmt.Errorf("unsupported qualified or duplicate migration table %s", r.Relname)
				}
				tables[r.Relname] = true
			}
		}
	}
	for table := range tables {
		if owners[table] == "" {
			return fmt.Errorf("migration table has no owner: %s", table)
		}
	}
	for table := range owners {
		if !tables[table] {
			return fmt.Errorf("owned table not created by migrations: %s", table)
		}
	}
	return nil
}

func checkMigrationBody(body string) error {
	// Routine bodies are opaque to the migration AST (including procedures).
	// Scan decoded AS strings and inline SQL bodies, ignoring actual comments
	// so CREATE/**/TABLE cannot evade the conservative text check.
	scan, err := pgquery.Scan(body)
	if err != nil {
		return fmt.Errorf("unsupported migration routine body: %w", err)
	}
	words := []string{}
	for _, token := range scan.Tokens {
		if token.Token == pgquery.Token_DO {
			return fmt.Errorf("unsupported migration DO block")
		}
		if token.Token != pgquery.Token_SQL_COMMENT && token.Token != pgquery.Token_C_COMMENT {
			words = append(words, body[token.Start:token.End])
		}
	}
	unsafe := regexp.MustCompile(`(?is)\bexecute\b|\bcreate\s+(?:(?:global|local)\s+)?(?:(?:temp|temporary|unlogged)\s+)?table\b|\balter\s+table\b.*\brename\b|\bdrop\s+table\b`)
	if unsafe.MatchString(strings.Join(words, " ")) {
		return fmt.Errorf("unsupported migration routine dynamic SQL or table DDL")
	}
	return nil
}
