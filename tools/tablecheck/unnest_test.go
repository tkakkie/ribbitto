package tablecheck

import (
	"strings"
	"testing"
)

func TestUnnestArguments(t *testing.T) {
	for _, tc := range []struct{ name, relation, want string }{
		{"other_function", "generate_series(1, 2)", "unsupported FROM function"},
		{"other_function_with_parameter", "lower(sqlc.arg(lo)::bigint[])", "unsupported FROM function"},
		{"reader_function", "unscoped_reader()", "unsupported FROM function"},
		{"qualified", "pg_catalog.unnest(sqlc.arg(lo)::bigint[])", "unsupported function unnest: requires unqualified FROM call"},
		{"no_arguments", "unnest()", "unsupported unnest call shape"},
		{"text_array", "unnest(sqlc.arg(lo)::text[])", "unsupported unnest argument"},
		{"scalar", "unnest(sqlc.arg(lo)::bigint)", "unsupported unnest argument"},
		{"uncast", "unnest(sqlc.arg(lo))", "unsupported unnest argument"},
		{"qualified_uuid", "unnest(sqlc.arg(lo)::pg_catalog.uuid[])", "unsupported unnest argument"},
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
			for _, arrayType := range []string{"bigint", "uuid"} {
				relation := strings.ReplaceAll(tc.relation, "bigint", arrayType)
				relation = strings.ReplaceAll(relation, "BIGINT", strings.ToUpper(arrayType))
				for _, sql := range []string{
					"SELECT * FROM " + relation,
					"WITH b AS (SELECT * FROM " + relation + ") SELECT * FROM b",
					"INSERT INTO message(id) SELECT 1 FROM " + relation,
				} {
					err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
					}
				}
			}
		})
	}
}

func TestUnnestRelations(t *testing.T) {
	call := "unnest(sqlc.arg(lo)::bigint[], sqlc.arg('hi')::BIGINT[])"
	for _, tc := range []struct{ name, sql, want string }{
		{"uuid_single", "SELECT * FROM unnest(sqlc.arg(id)::uuid[])", ""},
		{"uuid_parallel", "SELECT * FROM unnest(sqlc.arg(id)::uuid[], sqlc.arg('topic')::UUID[]) AS b(id, topic)", ""},
		{"uuid_bigint", "SELECT * FROM unnest(sqlc.arg(id)::uuid[], sqlc.arg(lo)::bigint[]) AS b(id, lo)", ""},
		{"single", "SELECT * FROM unnest(sqlc.arg(lo)::bigint[])", ""},
		{"parallel", "SELECT * FROM " + call + " AS b(lo, hi)", ""},
		{"CTE", "WITH b AS (SELECT * FROM " + call + " AS b(lo, hi)) SELECT * FROM b", ""},
		{"insert", "INSERT INTO message(id) SELECT lo FROM " + call + " AS b(lo, hi)", ""},
		{"insert_CTE_source", "WITH b AS (SELECT * FROM " + call + " AS b(lo, hi)) INSERT INTO message(id) SELECT lo FROM b", ""},
		{"CTE_insert", "WITH i AS (INSERT INTO message(id) SELECT lo FROM " + call + " AS b(lo, hi) RETURNING *) SELECT * FROM i", ""},
		{"scoped_join", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON m.id = b.lo WHERE m.organization_id = sqlc.arg(organization_id)", ""},
		{"update_FROM", "UPDATE message m SET id=b.lo FROM " + call + " b(lo, hi) WHERE m.organization_id = $1", ""},
		{"select_list", "SELECT " + call, "unsupported function unnest: requires unqualified FROM call"},
		{"beside_allowed", "SELECT " + call + " FROM " + call + " b(lo, hi)", "unsupported function unnest: requires unqualified FROM call"},
		{"where", "SELECT 1 WHERE " + call + " = 1", "unsupported function unnest: requires unqualified FROM call"},
		{"order_by", "SELECT 1 ORDER BY " + call, "unsupported function unnest: requires unqualified FROM call"},
		{"insert_target", "INSERT INTO message(id) SELECT " + call, "unsupported function unnest: requires unqualified FROM call"},
		{"values", "INSERT INTO message(id) VALUES (" + call + ")", "unsupported function unnest: requires unqualified FROM call"},
		{"returning", "INSERT INTO message(id) VALUES (1) RETURNING " + call, "unsupported function unnest: requires unqualified FROM call"},
		{"delete_USING", "DELETE FROM message m USING " + call + " b(lo, hi) WHERE m.organization_id = $1", "unsupported function unnest: requires unqualified FROM call"},
		{"foreign_write", "INSERT INTO account(id) SELECT lo FROM " + call + " b(lo, hi)", "foreign table account (write=true"},
		{"foreign_CTE_write", "WITH i AS (INSERT INTO account(id) SELECT lo FROM " + call + " b(lo, hi) RETURNING *) SELECT * FROM i", "foreign table account (write=true"},
		{"foreign_join", "SELECT a.* FROM " + call + " b(lo, hi) JOIN account a ON true", "foreign table account (write=false"},
		{"unknown_join", "SELECT a.* FROM " + call + " b(lo, hi) JOIN absent a ON true", "unknown table absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, callType := range []string{"bigint", "uuid", "mixed"} {
				sql := tc.sql
				if callType == "uuid" {
					sql = strings.ReplaceAll(sql, "bigint", "uuid")
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				} else if callType == "mixed" {
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				}
				err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
				}
			}
		})
	}
}
