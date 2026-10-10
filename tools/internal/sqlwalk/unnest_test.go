package sqlwalk

import (
	"strings"
	"testing"
)

func TestUnnestFromArguments(t *testing.T) {
	argument := "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"
	parameter := "unsupported unnest parameter name"
	call := "unsupported unnest call shape"
	relation := "unsupported unnest relation shape"
	for _, tc := range []struct{ name, relation, want string }{
		{"single", "unnest(sqlc.arg(lo)::bigint[])", ""},
		{"parallel", "unnest(sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[]) AS b(lo, hi)", ""},
		{"qualified", "pg_catalog.unnest(sqlc.arg(lo)::bigint[])", "unsupported function unnest: requires unqualified FROM call"},
		{"no_arguments", "unnest()", call},
		{"text_array", "unnest(sqlc.arg(lo)::text[])", argument},
		{"mixed", "unnest(sqlc.arg(id)::uuid[], sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[])", ""},
		{"scalar", "unnest(sqlc.arg(lo)::bigint)", argument},
		{"uncast", "unnest(sqlc.arg(lo))", argument},
		{"qualified_uuid", "unnest(sqlc.arg(lo)::pg_catalog.uuid[])", argument},
		{"explicit_type", "unnest(sqlc.arg(lo)::pg_catalog.int8[])", argument},
		{"type_alias", "unnest(sqlc.arg(lo)::int8[])", argument},
		{"dimensions", "unnest(sqlc.arg(lo)::bigint[][])", argument},
		{"sized_array", "unnest(sqlc.arg(lo)::bigint[2])", argument},
		{"column", "unnest(lo::bigint[])", argument},
		{"literal", "unnest('{1}'::bigint[])", argument},
		{"positional_parameter", "unnest($1::bigint[])", argument},
		{"nullable_parameter", "unnest(sqlc.narg(lo)::bigint[])", argument},
		{"computed", "unnest(lower(sqlc.arg(lo))::bigint[])", argument},
		{"subquery", "unnest((SELECT lo FROM message)::bigint[])", argument},
		{"mixed_arguments", "unnest(sqlc.arg(lo)::bigint[], hi::bigint[])", argument},
		{"computed_name", "unnest(sqlc.arg(lo + 1)::bigint[])", parameter},
		{"star_name", "unnest(sqlc.arg(*)::bigint[])", argument},
		{"qualified_name", "unnest(sqlc.arg(m.lo)::bigint[])", parameter},
		{"numeric_name", "unnest(sqlc.arg(1)::bigint[])", parameter},
		{"extra_name", "unnest(sqlc.arg(lo, hi)::bigint[])", argument},
		{"decorated_parameter", "unnest(sqlc.arg(DISTINCT lo)::bigint[])", argument},
		{"decorated_call", "unnest(DISTINCT sqlc.arg(lo)::bigint[])", call},
		{"lateral", "LATERAL unnest(sqlc.arg(lo)::bigint[])", relation},
		{"ordinality", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY", "unsupported unnest ordinality: requires alias with two columns"},
		{"rows_from", "ROWS FROM(unnest(sqlc.arg(lo)::bigint[]))", relation},
		{"column_definition", "unnest(sqlc.arg(lo)::bigint[]) AS b(lo bigint)", relation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, arrayType := range []string{"bigint", "uuid"} {
				relation := strings.ReplaceAll(tc.relation, "bigint", arrayType)
				relation = strings.ReplaceAll(relation, "BIGINT", strings.ToUpper(arrayType))
				for _, sql := range []string{
					"SELECT * FROM " + relation,
					"WITH b AS (SELECT * FROM " + relation + ") SELECT * FROM b",
					"INSERT INTO message(id) SELECT 1 FROM " + relation,
				} {
					checkUnnestFrom(t, sql, tc.want, 1)
				}
			}
		})
	}
}

func TestUnnestFromPositions(t *testing.T) {
	call := "unnest(sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[])"
	for _, tc := range []struct {
		name, sql string
		allowed   bool
		calls     int
	}{
		{"no_unnest", "SELECT 1", true, 0},
		{"other_function_is_caller_policy", "SELECT * FROM generate_series(1, 2)", true, 0},
		{"insert_CTE_source", "WITH b AS (SELECT * FROM " + call + " AS b(lo, hi)) INSERT INTO message(id) SELECT lo FROM b", true, 1},
		{"CTE_insert", "WITH i AS (INSERT INTO message(id) SELECT lo FROM " + call + " AS b(lo, hi) RETURNING *) SELECT * FROM i", true, 1},
		{"join", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON m.id = b.lo", true, 1},
		{"update_FROM", "UPDATE message m SET id=b.lo FROM " + call + " b(lo, hi)", true, 1},
		{"select_list", "SELECT " + call, false, 0},
		{"beside_allowed", "SELECT " + call + " FROM " + call + " b(lo, hi)", false, 0},
		{"where", "SELECT 1 WHERE " + call + " = 1", false, 0},
		{"order_by", "SELECT 1 ORDER BY " + call, false, 0},
		{"insert_target", "INSERT INTO message(id) SELECT " + call, false, 0},
		{"values", "INSERT INTO message(id) VALUES (" + call + ")", false, 0},
		{"returning", "INSERT INTO message(id) VALUES (1) RETURNING " + call, false, 0},
		{"delete_USING", "DELETE FROM message m USING " + call + " b(lo, hi)", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ""
			if !tc.allowed {
				want = "unsupported function unnest: requires unqualified FROM call"
			}
			for _, callType := range []string{"bigint", "uuid", "mixed"} {
				sql := tc.sql
				if callType == "uuid" {
					sql = strings.ReplaceAll(sql, "bigint", "uuid")
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				} else if callType == "mixed" {
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				}
				checkUnnestFrom(t, sql, want, tc.calls)
			}
		})
	}
}

func checkUnnestFrom(t *testing.T, sql, want string, count int) {
	t.Helper()
	tree, err := Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	bigints, err := BigintLocations(sql)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := UnnestFrom(tree, bigints)
	if want != "" {
		if err == nil || err.Error() != want {
			t.Fatalf("%s: want %q, got %v", sql, want, err)
		}
		return
	}
	if err != nil || len(calls) != count {
		t.Fatalf("%s: want %d calls, got %v, %v", sql, count, calls, err)
	}
	if err := Walk(tree, Scope{}, Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ Scope) error {
		if tag == "FuncCall" && Names(n["funcname"]) == "unnest" && !calls[n["location"].(float64)] {
			t.Errorf("%s: unnest location missing from %v", sql, calls)
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestUnnestOrdinalityArguments(t *testing.T) {
	for _, tc := range []struct{ name, relation, want string }{
		{"single", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value, n)", ""},
		{"uuid", "unnest(sqlc.arg('id')::UUID[]) WITH ORDINALITY AS a(value, n)", ""},
		{"multi_argument", "unnest(sqlc.arg(lo)::bigint[], sqlc.arg(hi)::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest ordinality: requires one argument"},
		{"no_alias", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY", "unsupported unnest ordinality: requires alias with two columns"},
		{"zero_columns", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a", "unsupported unnest ordinality: requires alias with two columns"},
		{"one_column", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value)", "unsupported unnest ordinality: requires alias with two columns"},
		{"three_columns", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value, n, extra)", "unsupported unnest ordinality: requires alias with two columns"},
		{"lateral", "LATERAL unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest relation shape"},
		{"rows_from", "ROWS FROM(unnest(sqlc.arg(lo)::bigint[])) WITH ORDINALITY AS a(value, n)", "unsupported unnest relation shape"},
		{"column_definition", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value bigint, n bigint)", "unsupported unnest relation shape"},
		{"column", "unnest(lo::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"literal", "unnest('{1}'::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"computed", "unnest(lower(sqlc.arg(lo))::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"positional", "unnest($1::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"text", "unnest(sqlc.arg(lo)::text[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"qualified_uuid", "unnest(sqlc.arg(lo)::pg_catalog.uuid[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
		{"sized", "unnest(sqlc.arg(lo)::bigint[2]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sql := range []string{
				"SELECT * FROM " + tc.relation,
				"WITH b AS (SELECT * FROM " + tc.relation + ") SELECT * FROM b",
				"INSERT INTO message(id) SELECT value FROM " + tc.relation,
			} {
				checkUnnestFrom(t, sql, tc.want, 1)
			}
		})
	}
}

func TestUnnestOrdinalityRelations(t *testing.T) {
	a := "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY AS a(value, n)"
	b := "unnest(sqlc.arg('hi')::uuid[]) WITH ORDINALITY AS b(value, n)"
	joined := a + " JOIN " + b + " ON a.n = b.n"
	tables := joined + " JOIN message m ON true JOIN member k ON true"
	for _, tc := range []struct{ name, sql, want string }{
		{"select_join", "SELECT a.value, b.value FROM " + joined, ""},
		{"beside_allowed", "SELECT unnest(sqlc.arg(lo)::bigint[]) FROM " + a, "unsupported function unnest: requires unqualified FROM call"},
		{"CTE_join", "WITH pairs AS (SELECT a.value, b.value AS id FROM " + joined + ") SELECT * FROM pairs", ""},
		{"insert_CTE_join", "WITH pairs AS (SELECT a.value, b.value AS id FROM " + joined + ") INSERT INTO message(id) SELECT id FROM pairs", ""},
		{"update_FROM", "UPDATE message m SET id=a.value FROM " + a + " WHERE m.organization_id = $1", ""},
		{"scoped_tables", "SELECT m.id FROM " + tables + " WHERE m.organization_id = $1 AND k.organization_id = $1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, arrayType := range []string{"mixed", "bigint", "uuid"} {
				sql := tc.sql
				if arrayType == "bigint" {
					sql = strings.ReplaceAll(sql, "uuid", "bigint")
				} else if arrayType == "uuid" {
					sql = strings.ReplaceAll(sql, "bigint", "uuid")
				}
				calls := 2
				if tc.name == "update_FROM" {
					calls = 1
				}
				checkUnnestFrom(t, sql, tc.want, calls)
			}
		})
	}
}
