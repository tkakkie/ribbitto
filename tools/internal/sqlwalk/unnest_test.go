package sqlwalk

import "testing"

func TestUnnestFromArguments(t *testing.T) {
	argument := "unsupported unnest argument: requires bigint[] sqlc.arg parameter"
	parameter := "unsupported unnest parameter name"
	call := "unsupported unnest call shape"
	relation := "unsupported unnest relation shape"
	for _, tc := range []struct{ name, relation, want string }{
		{"single", "unnest(sqlc.arg(lo)::bigint[])", ""},
		{"parallel", "unnest(sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[]) AS b(lo, hi)", ""},
		{"qualified", "pg_catalog.unnest(sqlc.arg(lo)::bigint[])", "unsupported function unnest: requires unqualified FROM call"},
		{"no_arguments", "unnest()", call},
		{"text_array", "unnest(sqlc.arg(lo)::text[])", argument},
		{"uuid_array", "unnest(sqlc.arg(lo)::uuid[])", argument},
		{"scalar", "unnest(sqlc.arg(lo)::bigint)", argument},
		{"uncast", "unnest(sqlc.arg(lo))", argument},
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
		{"ordinality", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY", relation},
		{"rows_from", "ROWS FROM(unnest(sqlc.arg(lo)::bigint[]))", relation},
		{"column_definition", "unnest(sqlc.arg(lo)::bigint[]) AS b(lo bigint)", relation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sql := range []string{
				"SELECT * FROM " + tc.relation,
				"WITH b AS (SELECT * FROM " + tc.relation + ") SELECT * FROM b",
				"INSERT INTO message(id) SELECT 1 FROM " + tc.relation,
			} {
				checkUnnestFrom(t, sql, tc.want, 1)
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
			checkUnnestFrom(t, tc.sql, want, tc.calls)
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
