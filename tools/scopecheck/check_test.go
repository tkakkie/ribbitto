package scopecheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pgquery "github.com/pganalyze/pg_query_go/v6"
)

func object(v any) map[string]any           { m, _ := v.(map[string]any); return m }
func list(v any) []any                      { a, _ := v.([]any); return a }
func node(v any, key string) map[string]any { return object(object(v)[key]) }
func names(v any) string {
	var parts []string
	for _, item := range list(v) {
		parts = append(parts, fmt.Sprint(node(item, "String")["sval"]))
	}
	return strings.Join(parts, ".")
}

func parse(sql string) (any, error) {
	s, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return nil, fmt.Errorf("parsing SQL: %w", err)
	}
	var tree any
	err = json.Unmarshal([]byte(s), &tree)
	return tree, err
}

// Walking every expression also checks statements in CTEs and subqueries;
// their predicates never contribute to the enclosing statement's scope.
func walk(v any, visit func(string, map[string]any) error) error {
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			if err := visit(key, object(child)); err != nil {
				return err
			}
			if key == "InsertStmt" { // INSERT reads are excluded; writable CTEs still need checking.
				child = object(child)["withClause"]
			}
			if err := walk(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := walk(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func parameter(v any) bool {
	if node(v, "ParamRef") != nil {
		return true
	}
	f := node(v, "FuncCall")
	return len(list(f["funcname"])) == 2 && names(f["funcname"]) == "sqlc.arg" && len(list(f["args"])) == 1
}

func columnRef(v any, want string) bool {
	fields := node(v, "ColumnRef")["fields"]
	return len(list(fields)) == strings.Count(want, ".")+1 && names(fields) == want
}

func conjunct(v any, column string) bool {
	b := node(v, "BoolExpr")
	if b["boolop"] == "AND_EXPR" {
		for _, arg := range list(b["args"]) {
			if conjunct(arg, column) {
				return true
			}
		}
		return false
	}
	e := node(v, "A_Expr")
	if e["kind"] != "AEXPR_OP" || names(e["name"]) != "=" {
		return false
	}
	return columnRef(e["lexpr"], column) && parameter(e["rexpr"]) ||
		columnRef(e["rexpr"], column) && parameter(e["lexpr"])
}

func check(sql string, tables map[string]string) error {
	tree, err := parse(sql)
	if err != nil {
		return err
	}
	return walk(tree, func(kind string, s map[string]any) error {
		if kind == "CommonTableExpr" {
			if _, shadows := tables[fmt.Sprint(s["ctename"])]; shadows {
				return fmt.Errorf("unsupported shape: CTE shadows table")
			}
		}
		if kind == "WithClause" && s["recursive"] == true {
			return fmt.Errorf("unsupported shape: recursive CTE")
		}
		if !strings.HasSuffix(kind, "Stmt") || kind[0] < 'A' || kind[0] > 'Z' {
			return nil
		}
		var refs []any
		switch kind {
		case "SelectStmt":
			if s["op"] != "SETOP_NONE" || s["valuesLists"] != nil || s["intoClause"] != nil {
				return fmt.Errorf("unsupported shape: SELECT operation")
			}
			refs = list(s["fromClause"])
		case "UpdateStmt":
			refs = append([]any{map[string]any{"RangeVar": s["relation"]}}, list(s["fromClause"])...)
		case "DeleteStmt":
			refs = append([]any{map[string]any{"RangeVar": s["relation"]}}, list(s["usingClause"])...)
		case "InsertStmt":
			return nil // INSERT ownership is outside this gate.
		default:
			return fmt.Errorf("unsupported shape: %s", kind)
		}
		aliases := map[string]string{}
		var relation func(any) error
		relation = func(v any) error {
			if j := node(v, "JoinExpr"); j != nil {
				if j["jointype"] != "JOIN_INNER" || j["alias"] != nil || j["isNatural"] == true || j["usingClause"] != nil {
					return fmt.Errorf("unsupported shape: join")
				}
				if err := relation(j["larg"]); err != nil {
					return err
				}
				return relation(j["rarg"])
			}
			r := node(v, "RangeVar")
			table, _ := r["relname"].(string)
			column, known := tables[table]
			if !known || r["schemaname"] != nil || r["catalogname"] != nil {
				return fmt.Errorf("unsupported shape: relation %q (including CTE result reads)", table)
			}
			alias := table
			if a := object(r["alias"]); a != nil {
				if a["colnames"] != nil {
					return fmt.Errorf("unsupported shape: renamed columns")
				}
				alias, _ = a["aliasname"].(string)
			}
			if _, exists := aliases[alias]; exists {
				return fmt.Errorf("unsupported shape: duplicate alias %s", alias)
			}
			aliases[alias] = column
			return nil
		}
		for _, ref := range refs {
			if err := relation(ref); err != nil {
				return err
			}
		}
		for alias, column := range aliases {
			if column == "" {
				continue
			}
			if !conjunct(s["whereClause"], alias+"."+column) &&
				(len(aliases) != 1 || !conjunct(s["whereClause"], column)) {
				return fmt.Errorf("missing scope: %s.%s", alias, column)
			}
		}
		return nil
	})
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The migration schema is the single source for owned tables: an
// organization_id column means organisation ownership. organization uses
// its primary key instead; setup is installation-wide despite its FK.
func schema(t *testing.T) map[string]string {
	t.Helper()
	tables := map[string]string{}
	paths, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("reading schema: %v", err)
	}
	for _, path := range paths {
		tree, err := parse(strings.Split(read(t, path), "-- +goose Down")[0])
		if err != nil {
			t.Fatal(err)
		}
		err = walk(tree, func(kind string, s map[string]any) error {
			if kind == "RenameStmt" || kind == "DropStmt" || kind == "TableLikeClause" || s["inhRelations"] != nil || s["ofTypename"] != nil {
				return fmt.Errorf("unsupported schema shape: %s", kind)
			}
			if kind == "AlterTableCmd" && (strings.Contains(fmt.Sprint(s["subtype"]), "Inherit") || strings.HasSuffix(fmt.Sprint(s["subtype"]), "Of")) {
				return fmt.Errorf("unsupported schema shape: inheritance")
			}
			if kind != "CreateStmt" && kind != "AlterTableStmt" {
				return nil
			}
			if object(s["relation"])["schemaname"] != nil {
				return fmt.Errorf("unsupported schema shape: qualified table")
			}
			table, _ := object(s["relation"])["relname"].(string)
			if kind == "CreateStmt" {
				tables[table] = ""
			}
			return walk(s, func(kind string, col map[string]any) error {
				if kind == "ColumnDef" && col["colname"] == "organization_id" {
					tables[table] = "organization_id"
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tables["organization"], tables["setup"] = "id", ""
	return tables
}

func exemptions(text string, queries map[string]string) (map[string]string, error) {
	allow := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		name, reason, ok := strings.Cut(line, " ")
		if !ok || strings.TrimSpace(reason) == "" || allow[name] != "" {
			return nil, fmt.Errorf("invalid allowlist entry: %q", line)
		}
		if _, exists := queries[name]; !exists {
			return nil, fmt.Errorf("stale allowlist entry: %s", name)
		}
		allow[name] = reason
	}
	return allow, nil
}

func TestProductionQueries(t *testing.T) {
	queries := map[string]string{}
	header := regexp.MustCompile(`(?m)^-- name: (\w+) :\w+[^\n]*`)
	err := filepath.WalkDir("../../db/queries", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		sql := read(t, path)
		matches := header.FindAllStringSubmatchIndex(sql, -1)
		if len(matches) == 0 {
			return fmt.Errorf("no queries in %s", path)
		}
		prefix, err := parse(sql[:matches[0][0]])
		if err != nil || len(list(object(prefix)["stmts"])) != 0 {
			return fmt.Errorf("unnamed SQL in %s: %v", path, err)
		}
		for i, match := range matches {
			name, end := sql[match[2]:match[3]], len(sql)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			if _, exists := queries[name]; exists {
				return fmt.Errorf("duplicate query: %s", name)
			}
			queries[name] = sql[match[1]:end]
		}
		return nil
	})
	if err != nil || len(queries) == 0 {
		t.Fatalf("reading queries: %v", err)
	}
	allow, err := exemptions(read(t, "allowlist.txt"), queries)
	if err != nil {
		t.Fatal(err)
	}
	tables := schema(t)
	for name, sql := range queries {
		t.Run(name, func(t *testing.T) {
			// Even exempt queries must parse; an exemption waives scope/shape only.
			if tree, err := parse(sql); err != nil || len(list(object(tree)["stmts"])) != 1 {
				t.Fatalf("expected one parsed statement: %v", err)
			}
			if allow[name] == "" {
				if err := check(sql, tables); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
