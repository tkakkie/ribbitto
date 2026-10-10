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
		{"ordinality", "unnest(sqlc.arg(lo)::bigint[]) WITH ORDINALITY", "unsupported unnest ordinality: requires alias with two columns"},
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
					err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
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
		{"unscoped_join", "SELECT m.* FROM " + call + " b(lo, hi) JOIN message m ON m.id = b.lo", "missing scope: m.organization_id"},
		{"scoped_two_tables", "SELECT m.* FROM " + call + " b(lo, hi), message m, member n WHERE m.organization_id = $1 AND n.organization_id = $1", ""},
		{"first_unscoped_table", "SELECT m.* FROM " + call + " b(lo, hi), message m, member n WHERE n.organization_id = $1", "missing scope: m.organization_id"},
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
			for _, callType := range []string{"bigint", "uuid", "mixed"} {
				sql := tc.sql
				if callType == "uuid" {
					sql = strings.ReplaceAll(sql, "bigint", "uuid")
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				} else if callType == "mixed" {
					sql = strings.ReplaceAll(sql, "BIGINT", "UUID")
				}
				err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
				}
			}
		})
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
		{"column", "unnest(lo::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"literal", "unnest('{1}'::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"computed", "unnest(lower(sqlc.arg(lo))::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"positional", "unnest($1::bigint[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"text", "unnest(sqlc.arg(lo)::text[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"qualified_uuid", "unnest(sqlc.arg(lo)::pg_catalog.uuid[]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"sized", "unnest(sqlc.arg(lo)::bigint[2]) WITH ORDINALITY AS a(value, n)", "unsupported unnest argument"},
		{"other_function", "generate_series(1, 2) WITH ORDINALITY AS a(value, n)", "unsupported shape"},
		{"lateral_other", "LATERAL generate_series(1, 2) AS a(value)", "unsupported shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sql := range []string{
				"SELECT * FROM " + tc.relation,
				"WITH b AS (SELECT * FROM " + tc.relation + ") SELECT * FROM b",
				"INSERT INTO message(id) SELECT value FROM " + tc.relation,
			} {
				err := check(sql, map[string]string{"message": "organization_id"})
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
				}
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
		{"insert_join", "INSERT INTO message(id) SELECT a.value FROM " + joined, "unsupported shape: INSERT SELECT relation"},
		{"insert_comma_join", "INSERT INTO message(id) SELECT a.value FROM " + a + ", " + b, "unsupported shape: INSERT SELECT join"},
		{"first_unscoped_table", "SELECT m.id FROM " + tables + " WHERE k.organization_id = $1", "missing scope: m.organization_id"},
		{"second_unscoped_table", "SELECT m.id FROM " + tables + " WHERE m.organization_id = $1", "missing scope: k.organization_id"},
		{"insert_unscoped_CTE", "WITH pairs AS (SELECT m.id FROM " + tables + " WHERE k.organization_id = $1) INSERT INTO message(id) SELECT id FROM pairs", "missing scope: m.organization_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, arrayType := range []string{"mixed", "bigint", "uuid"} {
				sql := tc.sql
				if arrayType == "bigint" {
					sql = strings.ReplaceAll(sql, "uuid", "bigint")
				} else if arrayType == "uuid" {
					sql = strings.ReplaceAll(sql, "bigint", "uuid")
				}
				err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
				}
			}
		})
	}
}
