// Package tablecheck checks direct SQL ownership without adding cgo to the application.
package tablecheck

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	pgquery "github.com/pganalyze/pg_query_go/v6"
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
	parsed, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return err
	}
	var tree struct {
		Stmts []struct{ Stmt map[string]any }
	}
	if err := json.Unmarshal([]byte(parsed), &tree); err != nil {
		return err
	}
	if len(tree.Stmts) != 1 {
		return fmt.Errorf("expected one statement")
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
	var walk func(any, map[string]bool) error
	walk = func(value any, ctes map[string]bool) error {
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				if err := walk(child, ctes); err != nil {
					return err
				}
			}
		case map[string]any:
			for tag, child := range v {
				// PostgreSQL node tags start uppercase; lowercase keys are fields.
				if tag[0] >= 'A' && tag[0] <= 'Z' {
					n := child.(map[string]any)
					switch tag {
					case "SelectStmt", "InsertStmt", "UpdateStmt", "DeleteStmt":
						if n["intoClause"] != nil {
							return fmt.Errorf("unsupported SELECT INTO")
						}
						ctes = maps.Clone(ctes)
						if with, ok := n["withClause"].(map[string]any); ok {
							if with["recursive"] == true {
								return fmt.Errorf("unsupported recursive CTE")
							}
							for _, item := range with["ctes"].([]any) {
								cte := item.(map[string]any)["CommonTableExpr"].(map[string]any)
								if err := walk(cte, ctes); err != nil {
									return err
								}
								ctes[cte["ctename"].(string)] = true
							}
							delete(n, "withClause")
						}
						if tag != "SelectStmt" {
							r := n["relation"].(map[string]any)
							if r["schemaname"] != nil || r["catalogname"] != nil {
								return fmt.Errorf("unsupported qualified write")
							}
							if err := access(r["relname"].(string), true); err != nil {
								return err
							}
							delete(n, "relation")
						}
					case "RangeVar":
						if n["schemaname"] != nil || n["catalogname"] != nil {
							return fmt.Errorf("unsupported qualified read")
						}
						table := n["relname"].(string)
						if !ctes[table] {
							if err := access(table, false); err != nil {
								return err
							}
						}
					case "FuncCall":
						parts := []string{}
						for _, part := range n["funcname"].([]any) {
							parts = append(parts, part.(map[string]any)["String"].(map[string]any)["sval"].(string))
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
					case "ResTarget", "ColumnRef", "A_Star", "A_Const", "String", "Integer", "ParamRef", "A_Expr", "BoolExpr", "NullTest", "SubLink", "RangeSubselect", "JoinExpr", "Alias", "TypeCast", "TypeName", "SortBy", "List", "CoalesceExpr", "MinMaxExpr", "OnConflictClause", "InferClause", "IndexElem":
					default:
						return fmt.Errorf("unsupported SQL node %s", tag)
					}
				}
				if err := walk(child, ctes); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(tree.Stmts[0].Stmt, map[string]bool{})
}

func checkExemptions(allow map[exemption]bool, stale bool) error {
	pending := regexp.MustCompile(`(?i)pending[^a-z0-9]+maintainer(?:[^a-z0-9]|$)`)
	maintainer := regexp.MustCompile(`(?i)maintainer`)
	provenance := regexp.MustCompile(`#[0-9]+|https://github\.com/[^/\s]+/[^/\s]+/pull/[0-9]+#(?:discussion_r|issuecomment-)[0-9]+|(?i:decision)[^a-zA-Z0-9]+[0-9]+`)
	seen := map[string]bool{}
	for e, used := range allow {
		key := e.query + "/" + e.table
		if e.query == "" || e.table == "" || strings.TrimSpace(e.reason) == "" || pending.MatchString(strings.TrimSpace(e.reason)) || maintainer.MatchString(e.reason) && !provenance.MatchString(e.reason) || seen[key] || stale && !used {
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
	allow := map[exemption]bool{}
	if err := checkExemptions(allow, false); err != nil {
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
	err = filepath.WalkDir("../../db/queries", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".sql" {
			return nil
		}
		rel, err := filepath.Rel("../../db/queries", path)
		if err != nil {
			return err
		}
		module := strings.Split(filepath.ToSlash(rel), "/")[0]
		if !modules[module] {
			return fmt.Errorf("%s: unregistered query directory", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return checkQueries(string(data), filepath.ToSlash(rel), module, owners, allow)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkExemptions(allow, true); err != nil {
		t.Fatal(err)
	}
}

func checkQueries(sql, file, module string, owners map[string]string, allow map[exemption]bool) error {
	annotation := regexp.MustCompile(`(?m)^-- name: (\w+) :\w+`)
	matches := annotation.FindAllStringSubmatchIndex(sql, -1)
	if len(matches) == 0 {
		return fmt.Errorf("%s: no named queries", file)
	}
	prefix, err := pgquery.ParseToJSON(sql[:matches[0][0]])
	if err != nil || strings.Contains(prefix, `"stmt"`) {
		return fmt.Errorf("%s: SQL before first query", file)
	}
	seen := map[string]bool{}
	for i, match := range matches {
		end := len(sql)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		query := file + ":" + sql[match[2]:match[3]]
		if seen[query] {
			return fmt.Errorf("duplicate query %s", query)
		}
		seen[query] = true
		if err := checkSQL(sql[match[0]:end], module, query, owners, allow); err != nil {
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
