package scopecheck

import (
	"strings"
	"testing"
)

func TestUnnestArguments(t *testing.T) {
	for _, tc := range []struct{ name, relation, want string }{
		{"other_function", "generate_series(1, 2)", "unsupported shape"},
		{"other_function_with_parameter", "lower(sqlc.arg(lo)::bigint[])", "unsupported shape"},
		{"reader_function", "unscoped_reader()", "unsupported shape"},
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
				err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
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
		{"unscoped_join", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON true", "missing scope: m.organization_id"},
		{"second_unscoped_table", "SELECT m.* FROM " + call + " b(lo, hi), message m, member n WHERE m.organization_id = $1", "missing scope: n.organization_id"},
		{"join_ON", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON m.organization_id = $1", "missing scope"},
		{"unqualified_scope", "SELECT m.* FROM " + call + " b(lo, hi), message m WHERE organization_id = $1", "missing scope"},
		{"duplicate_alias", "SELECT m.* FROM " + call + " m(lo, hi), message m WHERE m.organization_id = $1", "duplicate alias"},
		{"outer_join", "SELECT m.* FROM " + call + " b(lo, hi) LEFT JOIN message m ON true WHERE m.organization_id = $1", "unsupported shape: join"},
		{"insert_join", "INSERT INTO message(id) SELECT b.lo FROM " + call + " b(lo, hi) JOIN member m ON true WHERE m.organization_id = $1", "unsupported shape: INSERT SELECT relation"},
		{"insert_comma_join", "INSERT INTO message(id) SELECT b.lo FROM " + call + " b(lo, hi), member m WHERE m.organization_id = $1", "unsupported shape: INSERT SELECT join"},
		{"insert_subquery", "INSERT INTO message(id) SELECT (SELECT id FROM member WHERE organization_id = $1) FROM " + call + " b(lo, hi)", "unsupported shape: INSERT subquery"},
		{"insert_unscoped_CTE", "WITH b AS (SELECT m.id FROM " + call + " b(lo, hi) JOIN member m ON true) INSERT INTO message(id) SELECT id FROM b", "missing scope: m.organization_id"},
		{"update_unscoped", "UPDATE message m SET id=b.lo FROM " + call + " b(lo, hi)", "missing scope: m.organization_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"message": "organization_id", "member": "organization_id"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
