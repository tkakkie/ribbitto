package tablecheck

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// Only a value parameter may carry read state across the module boundary.
// Permission belongs to this cast, never to another nearby cast or call.
func readSetParameter(v any) bool {
	cast := sqlwalk.Node(v, "TypeCast")
	typ := sqlwalk.Object(cast["typeName"])
	if len(sqlwalk.List(typ["names"])) != 1 || sqlwalk.Names(typ["names"]) != "int8multirange" ||
		typ["arrayBounds"] != nil || typ["typmods"] != nil || typ["setof"] == true {
		return false
	}
	f := sqlwalk.Node(cast["arg"], "FuncCall")
	args := sqlwalk.List(f["args"])
	if len(f) != 4 || len(sqlwalk.List(f["funcname"])) != 2 || sqlwalk.Names(f["funcname"]) != "sqlc.arg" ||
		f["funcformat"] != "COERCE_EXPLICIT_CALL" || len(args) != 1 {
		return false
	}
	fields := sqlwalk.List(sqlwalk.Node(args[0], "ColumnRef")["fields"])
	literal := sqlwalk.Object(sqlwalk.Node(args[0], "A_Const")["sval"])["sval"]
	return len(fields) == 1 && sqlwalk.Node(fields[0], "String")["sval"] != nil || literal != nil
}

// sqlc resolves column types against the schema. Other expressions must declare
// bigint explicitly; the ordinary walk still checks their casts, calls and reads.
func bigintOperand(v any, bigints map[float64]bool) bool {
	if column := sqlwalk.Node(v, "ColumnRef"); column != nil {
		for _, field := range sqlwalk.List(column["fields"]) {
			if sqlwalk.Node(field, "String") == nil {
				return false
			}
		}
		return true
	}
	typ := sqlwalk.Object(sqlwalk.Node(v, "TypeCast")["typeName"])
	return len(sqlwalk.List(typ["names"])) == 2 && sqlwalk.Names(typ["names"]) == "pg_catalog.int8" &&
		bigints[typ["location"].(float64)] && typ["arrayBounds"] == nil && typ["typmods"] == nil && typ["setof"] != true
}

func TestReadSetExpressions(t *testing.T) {
	p := "sqlc.arg(read_set)::int8multirange"
	for _, tc := range []struct{ name, expr, want string }{
		{"scalar", p, ""},
		{"string_name", "sqlc.arg('read_set')::INT8MULTIRANGE", ""},
		{"contains_column", p + " @> m.event_seq", ""},
		{"not_contains", "NOT (" + p + " @> m.event_seq)", ""},
		{"contains_expression", p + " @> (m.event_seq + 1)::bigint", ""},
		{"contains_parameter", p + " @> sqlc.arg(seq)::bigint", ""},
		{"int8range", "int8range(1, 2)", "unsupported function int8range"},
		{"constructor", "int8multirange()", "unsupported function int8multirange"},
		{"qualified_constructor", "pg_catalog.int8multirange()", "unsupported function pg_catalog.int8multirange"},
		{"range_agg", "range_agg(m.event_seq)", "unsupported function range_agg"},
		{"other_range", "sqlc.arg(read_set)::int8range", "unsupported type int8range"},
		{"other_multirange", "sqlc.arg(read_set)::nummultirange", "unsupported type nummultirange"},
		{"qualified_type", "sqlc.arg(read_set)::pg_catalog.int8multirange", "unsupported type pg_catalog"},
		{"array", p + "[]", "unsupported type int8multirange"},
		{"typmod", p + "(1)", "unsupported type int8multirange"},
		{"nullable", "sqlc.narg(read_set)::int8multirange", "unsupported type int8multirange"},
		{"column_cast", "m.read_set::int8multirange", "unsupported type int8multirange"},
		{"literal_cast", "'{}'::int8multirange", "unsupported type int8multirange"},
		{"positional", "$1::int8multirange", "unsupported type int8multirange"},
		{"computed_cast", "lower(sqlc.arg(read_set))::int8multirange", "unsupported type int8multirange"},
		{"computed_name", "sqlc.arg(x + 1)::int8multirange", "unsupported type int8multirange"},
		{"qualified_name", "sqlc.arg(m.x)::int8multirange", "unsupported type int8multirange"},
		{"numeric_name", "sqlc.arg(1)::int8multirange", "unsupported type int8multirange"},
		{"extra_name", "sqlc.arg(x, y)::int8multirange", "unsupported type int8multirange"},
		{"decorated", "sqlc.arg(DISTINCT x)::int8multirange", "unsupported type int8multirange"},
		{"contained_by", "m.event_seq <@ " + p, "unsupported operator <@"},
		{"overlap", p + " && " + p, "unsupported operator &&"},
		{"other_operator", p + " -|- " + p, "unsupported operator -|-"},
		{"qualified_operator", p + " OPERATOR(pg_catalog.@>) m.event_seq", "unsupported qualified operator"},
		{"left_column", "m.read_set @> m.event_seq", "unsupported containment"},
		{"left_uncast", "sqlc.arg(read_set) @> m.event_seq", "unsupported containment"},
		{"left_computed", "COALESCE(" + p + ", " + p + ") @> m.event_seq", "unsupported containment"},
		{"right_star", p + " @> m.*", "unsupported containment"},
		{"right_uuid", p + " @> sqlc.arg(seq)::uuid", "unsupported containment"},
		{"right_array", p + " @> sqlc.arg(seq)::bigint[]", "unsupported containment"},
		{"right_qualified", p + " @> sqlc.arg(seq)::pg_catalog.int8", "unsupported containment"},
		{"right_uncast", p + " @> sqlc.arg(seq)", "unsupported containment"},
		{"hidden_function", p + " @> hidden_reader()::bigint", "unsupported function hidden_reader"},
		{"unknown_node", p + " @> (CASE WHEN true THEN 1 END)::bigint", "unsupported SQL node CaseExpr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The accepted cast must not grant permission to a refused neighbour.
			sql := "SELECT " + p + ", " + tc.expr + " FROM message m"
			err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation"}, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestReadSetOwnership(t *testing.T) {
	filter := "NOT (sqlc.arg(read_set)::int8multirange @> m.event_seq)"
	for _, tc := range []struct{ name, sql, want string }{
		{"CTE", "WITH c AS (SELECT * FROM message m WHERE " + filter + ") SELECT * FROM c", ""},
		{"foreign_read", "SELECT * FROM account m WHERE " + filter, "foreign table account (write=false"},
		{"foreign_write", "UPDATE account m SET id=1 WHERE " + filter, "foreign table account (write=true"},
		{"foreign_subquery", "SELECT * FROM message m WHERE " + filter + " AND EXISTS (SELECT 1 FROM account)", "foreign table account (write=false"},
		{"unknown_table", "SELECT * FROM absent m WHERE " + filter, "unknown table absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSQL(tc.sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
