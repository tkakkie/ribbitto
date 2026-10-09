package tablecheck

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// Only FROM calls are eligible: function arguments and other expression positions
// must never inherit permission from a nearby allowed unnest.
func unnestFrom(tree any, bigints map[float64]bool) (map[float64]bool, error) {
	calls := map[float64]bool{}
	var from func(any) error
	from = func(v any) error {
		if j := sqlwalk.Node(v, "JoinExpr"); j != nil {
			if err := from(j["larg"]); err != nil {
				return err
			}
			return from(j["rarg"])
		}
		r := sqlwalk.Node(v, "RangeFunction")
		functions := sqlwalk.List(r["functions"])
		if len(functions) != 1 {
			return nil
		}
		items := sqlwalk.List(sqlwalk.Node(functions[0], "List")["items"])
		if len(items) != 2 {
			return nil
		}
		f := sqlwalk.Node(items[0], "FuncCall")
		if len(sqlwalk.List(f["funcname"])) != 1 || sqlwalk.Names(f["funcname"]) != "unnest" {
			return nil
		}
		if r["lateral"] == true || r["ordinality"] == true || r["is_rowsfrom"] == true || r["coldeflist"] != nil || len(sqlwalk.Object(items[1])) != 0 {
			return fmt.Errorf("unsupported unnest relation shape")
		}
		// Reject call decorations rather than inheriting aggregate or window semantics.
		if len(f) != 4 || f["funcformat"] != "COERCE_EXPLICIT_CALL" || len(sqlwalk.List(f["args"])) == 0 {
			return fmt.Errorf("unsupported unnest call shape")
		}
		for _, arg := range sqlwalk.List(f["args"]) {
			cast := sqlwalk.Node(arg, "TypeCast")
			typ := sqlwalk.Object(cast["typeName"])
			bounds := sqlwalk.List(typ["arrayBounds"])
			if len(sqlwalk.List(typ["names"])) != 2 || sqlwalk.Names(typ["names"]) != "pg_catalog.int8" || !bigints[typ["location"].(float64)] ||
				len(bounds) != 1 || sqlwalk.Node(bounds[0], "Integer")["ival"] != float64(-1) || typ["typmods"] != nil || typ["setof"] == true {
				return fmt.Errorf("unsupported unnest argument: requires bigint[] sqlc.arg parameter")
			}
			parameter := sqlwalk.Node(cast["arg"], "FuncCall")
			args := sqlwalk.List(parameter["args"])
			if len(parameter) != 4 || len(sqlwalk.List(parameter["funcname"])) != 2 || sqlwalk.Names(parameter["funcname"]) != "sqlc.arg" ||
				parameter["funcformat"] != "COERCE_EXPLICIT_CALL" || len(args) != 1 {
				return fmt.Errorf("unsupported unnest argument: requires bigint[] sqlc.arg parameter")
			}
			fields := sqlwalk.List(sqlwalk.Node(args[0], "ColumnRef")["fields"])
			literal := sqlwalk.Object(sqlwalk.Node(args[0], "A_Const")["sval"])["sval"]
			if !(len(fields) == 1 && sqlwalk.Node(fields[0], "String")["sval"] != nil) && literal == nil {
				return fmt.Errorf("unsupported unnest parameter name")
			}
		}
		calls[f["location"].(float64)] = true
		return nil
	}
	err := sqlwalk.Walk(tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
		if tag == "SelectStmt" || tag == "UpdateStmt" {
			for _, ref := range sqlwalk.List(n["fromClause"]) {
				if err := from(ref); err != nil {
					return err
				}
			}
		}
		return nil
	}})
	if err != nil {
		return nil, err
	}
	err = sqlwalk.Walk(tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
		if tag == "FuncCall" {
			parts := sqlwalk.List(n["funcname"])
			if len(parts) > 0 && sqlwalk.Node(parts[len(parts)-1], "String")["sval"] == "unnest" && !calls[n["location"].(float64)] {
				return fmt.Errorf("unsupported function unnest: requires unqualified FROM call")
			}
		}
		return nil
	}})
	return calls, err
}

func TestUnnestArguments(t *testing.T) {
	for _, tc := range []struct{ name, relation, want string }{
		{"other_function", "generate_series(1, 2)", "unsupported FROM function"},
		{"other_function_with_parameter", "lower(sqlc.arg(lo)::bigint[])", "unsupported FROM function"},
		{"reader_function", "unscoped_reader()", "unsupported FROM function"},
		{"qualified", "pg_catalog.unnest(sqlc.arg(lo)::bigint[])", "unsupported function unnest"},
		{"no_arguments", "unnest()", "unsupported unnest call shape"},
		{"text_array", "unnest(sqlc.arg(lo)::text[])", "unsupported unnest argument"},
		{"uuid_array", "unnest(sqlc.arg(lo)::uuid[])", "unsupported unnest argument"},
		{"scalar", "unnest(sqlc.arg(lo)::bigint)", "unsupported unnest argument"},
		{"uncast", "unnest(sqlc.arg(lo))", "unsupported unnest argument"},
		{"explicit_type", "unnest(sqlc.arg(lo)::pg_catalog.int8[])", "unsupported unnest argument"},
		{"type_alias", "unnest(sqlc.arg(lo)::int8[])", "unsupported unnest argument"},
		{"dimensions", "unnest(sqlc.arg(lo)::bigint[][])", "unsupported unnest argument"},
		{"sized_array", "unnest(sqlc.arg(lo)::bigint[2])", "unsupported unnest argument"},
		{"column", "unnest(lo::bigint[])", "unsupported unnest argument"},
		{"literal", "unnest('{1}'::bigint[])", "unsupported unnest argument"},
		{"positional_parameter", "unnest($1::bigint[])", "unsupported unnest argument"},
		{"nullable_parameter", "unnest(sqlc.narg(lo)::bigint[])", "unsupported unnest argument"},
		{"computed", "unnest(lower(sqlc.arg(lo))::bigint[])", "unsupported unnest argument"},
		{"subquery", "unnest((SELECT lo FROM message)::bigint[])", "unsupported unnest argument"},
		{"mixed_arguments", "unnest(sqlc.arg(lo)::bigint[], hi::bigint[])", "unsupported unnest argument"},
		{"computed_name", "unnest(sqlc.arg(lo + 1)::bigint[])", "unsupported unnest parameter name"},
		{"star_name", "unnest(sqlc.arg(*)::bigint[])", "unsupported unnest argument"},
		{"qualified_name", "unnest(sqlc.arg(m.lo)::bigint[])", "unsupported unnest parameter name"},
		{"numeric_name", "unnest(sqlc.arg(1)::bigint[])", "unsupported unnest parameter name"},
		{"extra_name", "unnest(sqlc.arg(lo, hi)::bigint[])", "unsupported unnest argument"},
		{"decorated_parameter", "unnest(sqlc.arg(DISTINCT lo)::bigint[])", "unsupported unnest argument"},
		{"decorated_call", "unnest(DISTINCT sqlc.arg(lo)::bigint[])", "unsupported unnest call shape"},
		{"lateral", "LATERAL unnest(sqlc.arg(lo)::bigint[])", "unsupported unnest relation shape"},
		{"ordinality", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY", "unsupported unnest relation shape"},
		{"rows_from", "ROWS FROM(unnest(sqlc.arg(lo)::bigint[]))", "unsupported unnest relation shape"},
		{"column_definition", "unnest(sqlc.arg(lo)::bigint[]) AS b(lo bigint)", "unsupported unnest relation shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sql := range []string{
				"SELECT * FROM " + tc.relation,
				"WITH b AS (SELECT * FROM " + tc.relation + ") SELECT * FROM b",
				"INSERT INTO message(id) SELECT 1 FROM " + tc.relation,
			} {
				err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
				}
			}
		})
	}
}

func TestUnnestRelations(t *testing.T) {
	call := "unnest(sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[])"
	for _, tc := range []struct{ name, sql, want string }{
		{"single", "SELECT * FROM unnest(sqlc.arg(lo)::bigint[])", ""},
		{"parallel", "SELECT * FROM " + call + " AS b(lo, hi)", ""},
		{"CTE", "WITH b AS (SELECT * FROM " + call + " AS b(lo, hi)) SELECT * FROM b", ""},
		{"insert", "INSERT INTO message(id) SELECT lo FROM " + call + " AS b(lo, hi)", ""},
		{"insert_CTE_source", "WITH b AS (SELECT * FROM " + call + " AS b(lo, hi)) INSERT INTO message(id) SELECT lo FROM b", ""},
		{"CTE_insert", "WITH i AS (INSERT INTO message(id) SELECT lo FROM " + call + " AS b(lo, hi) RETURNING *) SELECT * FROM i", ""},
		{"scoped_join", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON m.id = b.lo WHERE m.organization_id = sqlc.arg(organization_id)", ""},
		{"update_FROM", "UPDATE message m SET id=b.lo FROM " + call + " b(lo, hi) WHERE m.organization_id = $1", ""},
		{"select_list", "SELECT " + call, "unsupported function unnest"},
		{"beside_allowed", "SELECT " + call + " FROM " + call + " b(lo, hi)", "unsupported function unnest"},
		{"where", "SELECT 1 WHERE " + call + " = 1", "unsupported function unnest"},
		{"order_by", "SELECT 1 ORDER BY " + call, "unsupported function unnest"},
		{"insert_target", "INSERT INTO message(id) SELECT " + call, "unsupported function unnest"},
		{"values", "INSERT INTO message(id) VALUES (" + call + ")", "unsupported function unnest"},
		{"returning", "INSERT INTO message(id) VALUES (1) RETURNING " + call, "unsupported function unnest"},
		{"delete_USING", "DELETE FROM message m USING " + call + " b(lo, hi) WHERE m.organization_id = $1", "unsupported function unnest"},
		{"foreign_write", "INSERT INTO account(id) SELECT lo FROM " + call + " b(lo, hi)", "foreign table account (write=true"},
		{"foreign_CTE_write", "WITH i AS (INSERT INTO account(id) SELECT lo FROM " + call + " b(lo, hi) RETURNING *) SELECT * FROM i", "foreign table account (write=true"},
		{"foreign_join", "SELECT a.* FROM " + call + " b(lo, hi) JOIN account a ON true", "foreign table account (write=false"},
		{"unknown_join", "SELECT a.* FROM " + call + " b(lo, hi) JOIN absent a ON true", "unknown table absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSQL(tc.sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
