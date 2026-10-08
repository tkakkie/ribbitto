package scopecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// INSERT bodies are checked by the scope policy; WITH must be visited first.
func walk(v any, visit func(string, map[string]any) error) error {
	return sqlwalk.Walk(v, sqlwalk.Scope{}, sqlwalk.Options{OpaqueInserts: true,
		Visit: func(kind string, n map[string]any, _ sqlwalk.Scope) error { return visit(kind, n) },
	})
}

func parameter(v any) bool {
	if sqlwalk.Node(v, "ParamRef") != nil {
		return true
	}
	f := sqlwalk.Node(v, "FuncCall")
	return len(sqlwalk.List(f["funcname"])) == 2 && sqlwalk.Names(f["funcname"]) == "sqlc.arg" && len(sqlwalk.List(f["args"])) == 1
}

func columnRef(v any, want string) bool {
	fields := sqlwalk.Node(v, "ColumnRef")["fields"]
	return len(sqlwalk.List(fields)) == strings.Count(want, ".")+1 && sqlwalk.Names(fields) == want
}

func conjunct(v any, column string) bool {
	b := sqlwalk.Node(v, "BoolExpr")
	if b["boolop"] == "AND_EXPR" {
		for _, arg := range sqlwalk.List(b["args"]) {
			if conjunct(arg, column) {
				return true
			}
		}
		return false
	}
	e := sqlwalk.Node(v, "A_Expr")
	if e["kind"] != "AEXPR_OP" || sqlwalk.Names(e["name"]) != "=" {
		return false
	}
	return columnRef(e["lexpr"], column) && parameter(e["rexpr"]) ||
		columnRef(e["rexpr"], column) && parameter(e["lexpr"])
}

func check(sql string, tables map[string]string) error {
	tree, err := sqlwalk.Parse(sql)
	if err != nil {
		return err
	}
	for _, raw := range sqlwalk.Statements(tree) {
		if err := checkStatement(sqlwalk.Object(raw)["stmt"], tables); err != nil {
			return err
		}
	}
	return nil
}

func checkStatement(tree any, tables map[string]string) error {
	ctes, err := sqlwalk.CollectCTEs(tree)
	if err != nil {
		return err
	}
	for name := range ctes {
		if _, shadows := tables[name]; shadows {
			return fmt.Errorf("unsupported shape: CTE shadows table")
		}
	}
	return walk(tree, func(kind string, s map[string]any) error {
		if !strings.HasSuffix(kind, "Stmt") || !sqlwalk.IsNode(kind) {
			return nil
		}
		var refs []any
		switch kind {
		case "SelectStmt":
			if s["op"] != "SETOP_NONE" || s["valuesLists"] != nil || s["intoClause"] != nil {
				return fmt.Errorf("unsupported shape: SELECT operation")
			}
			refs = sqlwalk.List(s["fromClause"])
		case "UpdateStmt":
			table, _ := sqlwalk.Object(s["relation"])["relname"].(string)
			if column := tables[table]; column != "" {
				// A scoped WHERE cannot prevent SET from moving the row's ownership.
				for _, target := range sqlwalk.List(s["targetList"]) {
					if sqlwalk.Node(target, "ResTarget")["name"] == column {
						return fmt.Errorf("scope assignment: %s.%s", table, column)
					}
				}
			}
			refs = append([]any{map[string]any{"RangeVar": s["relation"]}}, sqlwalk.List(s["fromClause"])...)
		case "DeleteStmt":
			refs = append([]any{map[string]any{"RangeVar": s["relation"]}}, sqlwalk.List(s["usingClause"])...)
		case "InsertStmt":
			if err := noSubquery(s["returningList"]); err != nil {
				return err
			}
			values := sqlwalk.Node(s["selectStmt"], "SelectStmt")
			// Conflict handling and overriding remain outside both exemptions.
			if s["onConflictClause"] != nil || s["override"] != "OVERRIDING_NOT_SET" {
				return fmt.Errorf("unsupported shape: INSERT")
			}
			if len(values) == 3 && values["valuesLists"] != nil && values["op"] == "SETOP_NONE" &&
				values["limitOption"] == "LIMIT_OPTION_DEFAULT" {
				return noSubquery(values["valuesLists"])
			}
			return insertCTESelect(values, ctes)
		default:
			return fmt.Errorf("unsupported shape: %s", kind)
		}
		aliases := map[string]string{}
		var relation func(any) error
		relation = func(v any) error {
			if j := sqlwalk.Node(v, "JoinExpr"); j != nil {
				if j["jointype"] != "JOIN_INNER" || j["alias"] != nil || j["isNatural"] == true || j["usingClause"] != nil {
					return fmt.Errorf("unsupported shape: join")
				}
				if err := relation(j["larg"]); err != nil {
					return err
				}
				return relation(j["rarg"])
			}
			r := sqlwalk.Node(v, "RangeVar")
			table, _ := r["relname"].(string)
			column, known := tables[table]
			known = known || !(sqlwalk.Scope{CTEs: ctes}).Physical(r)
			if !known || r["schemaname"] != nil || r["catalogname"] != nil {
				return fmt.Errorf("unsupported shape: relation %q", table)
			}
			alias := table
			if a := sqlwalk.Object(r["alias"]); a != nil {
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

// CTE bodies have their own scope checks. This exemption must not hide
// additional reads in the INSERT's SELECT, even when those reads are scoped.
func insertCTESelect(s map[string]any, ctes map[string]bool) error {
	if s == nil || s["op"] != "SETOP_NONE" || s["valuesLists"] != nil || s["intoClause"] != nil || s["withClause"] != nil {
		return fmt.Errorf("unsupported shape: INSERT SELECT")
	}
	fromClause := sqlwalk.List(s["fromClause"])
	if len(fromClause) > 1 {
		return fmt.Errorf("unsupported shape: INSERT SELECT join")
	}
	for _, ref := range fromClause {
		r := sqlwalk.Node(ref, "RangeVar")
		table, _ := r["relname"].(string)
		if (sqlwalk.Scope{CTEs: ctes}).Physical(r) {
			return fmt.Errorf("unsupported shape: INSERT SELECT relation %q", table)
		}
	}
	return noSubquery(s)
}

// These INSERT exemptions allow no nested reads, even with scope predicates.
func noSubquery(v any) error {
	return walk(v, func(kind string, _ map[string]any) error {
		if kind == "SubLink" || strings.HasSuffix(kind, "Stmt") {
			return fmt.Errorf("unsupported shape: INSERT subquery")
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

// Migration columns establish ownership, except organization's primary key
// and the explicit installation-wide list. Classify after all
// migrations so a later ADD COLUMN can establish ownership.
func schema(migrations []string) (map[string]string, error) {
	tables := map[string]string{}
	created := map[string]bool{}
	for _, migration := range migrations {
		tree, err := sqlwalk.Parse(strings.Split(migration, "-- +goose Down")[0])
		if err != nil {
			return nil, err
		}
		err = walk(tree, func(kind string, s map[string]any) error {
			if kind == "RenameStmt" && (s["renameType"] == "OBJECT_TABLE" || s["renameType"] == "OBJECT_COLUMN") ||
				kind == "DropStmt" && s["removeType"] == "OBJECT_TABLE" ||
				kind == "TableLikeClause" || s["inhRelations"] != nil || s["ofTypename"] != nil {
				return fmt.Errorf("unsupported schema shape: %s", kind)
			}
			if kind == "AlterTableCmd" && (strings.Contains(fmt.Sprint(s["subtype"]), "Inherit") || strings.HasSuffix(fmt.Sprint(s["subtype"]), "Of")) {
				return fmt.Errorf("unsupported schema shape: inheritance")
			}
			if kind != "CreateStmt" && kind != "AlterTableStmt" {
				return nil
			}
			if sqlwalk.Object(s["relation"])["schemaname"] != nil {
				return fmt.Errorf("unsupported schema shape: qualified table")
			}
			table, _ := sqlwalk.Object(s["relation"])["relname"].(string)
			if kind == "CreateStmt" {
				created[table] = true
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
			return nil, err
		}
	}
	installationWide := map[string]bool{
		"account": true, // Identities exist independently of organisation membership.
		"session": true, // Authentication sessions belong to installation-wide accounts.
		"setup":   true, // The installation's singleton setup row resolves its home organisation.
	}
	for table, column := range tables {
		switch {
		case table == "organization":
			tables[table] = "id"
		case installationWide[table]:
			if column != "" && table != "setup" {
				return nil, fmt.Errorf("listed installation-wide but scoped: %s", table)
			}
			tables[table] = ""
		case column != "organization_id":
			return nil, fmt.Errorf("unknown ownership: %s", table)
		}
	}
	for table := range installationWide {
		if !created[table] {
			return nil, fmt.Errorf("stale installation-wide entry: %s", table)
		}
	}
	return tables, nil
}

func exemptions(text string, queries, tables map[string]string) (map[string]string, error) {
	allow := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, reason, ok := strings.Cut(line, " ")
		if !ok || strings.TrimSpace(reason) == "" || allow[name] != "" {
			return nil, fmt.Errorf("invalid allowlist entry: %q", line)
		}
		if err := (sqlwalk.ReasonRules{PendingColon: true}).Check(reason); err != nil {
			return nil, fmt.Errorf("%w: %s", err, name)
		}
		if _, exists := queries[name]; !exists {
			return nil, fmt.Errorf("stale allowlist entry: %s", name)
		}
		if check(queries[name], tables) == nil {
			return nil, fmt.Errorf("unnecessary allowlist entry: %s", name)
		}
		allow[name] = reason
	}
	return allow, nil
}

func TestProductionQueries(t *testing.T) {
	queries, err := sqlwalk.Load("../../db/queries", nil)
	if err != nil {
		t.Fatalf("reading queries: %v", err)
	}
	paths, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("reading schema: %v", err)
	}
	var migrations []string
	for _, path := range paths {
		migrations = append(migrations, read(t, path))
	}
	tables, err := schema(migrations)
	if err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]string{"member": "organization_id", "event_log": "organization_id", "organization": "id"} {
		if tables[table] != want {
			t.Fatalf("%s scope: want %q, got %q", table, want, tables[table])
		}
	}
	allow, err := exemptions(read(t, "allowlist.txt"), queries, tables)
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range queries {
		t.Run(name, func(t *testing.T) {
			// Even exempt queries must parse; an exemption waives scope/shape only.
			if tree, err := sqlwalk.Parse(sql); err != nil || len(sqlwalk.Statements(tree)) != 1 {
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
