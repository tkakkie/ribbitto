package scopecheck

import (
	"strings"
	"testing"
)

func TestReadSetArrayScope(t *testing.T) {
	relation := "unnest(sqlc.arg(read_sets)::int8multirange[]) WITH ORDINALITY AS c(read_set, ord)"
	joined := "unnest(sqlc.arg(channel_ids)::uuid[]) WITH ORDINALITY AS a(channel_id, ord) JOIN unnest(sqlc.arg(positions)::bigint[]) WITH ORDINALITY AS b(p, ord) ON b.ord = a.ord JOIN " + relation + " ON c.ord = a.ord JOIN message m ON m.channel_id = a.channel_id"
	query := "SELECT m.id FROM " + joined + " WHERE m.organization_id = sqlc.arg(organization_id) AND NOT (c.read_set @> m.event_seq::bigint)"
	for _, tc := range []struct{ name, sql, want string }{
		{"ordinal_join", query, ""},
		{"aliased_join_clash", "SELECT 1 FROM (message c JOIN message m ON c.read_set @> m.event_seq) j, " + relation, "unsupported shape: join"},
		{"aliased_join_clash_cast", "SELECT 1 FROM (message c JOIN message m ON c.read_set @> sqlc.arg(seq)::bigint) j, " + relation, "unsupported shape: join"},
		{"implicit_unnest_clash", "SELECT unnest.unnest @> 1::bigint FROM unnest(sqlc.arg(ids)::uuid[]), " + strings.ReplaceAll(relation, "c(read_set, ord)", "unnest(unnest, ord)"), "unsupported shape: duplicate alias unnest"},
		{"only_relation", "SELECT c.read_set @> sqlc.arg(seq)::bigint FROM " + relation, ""},
		{"CTE", "WITH x AS (" + query + ") SELECT * FROM x", ""},
		{"derived", "SELECT * FROM (" + query + ") x", ""},
		{"update", "UPDATE message m SET body='x' FROM " + relation + " WHERE m.organization_id=$1 AND c.read_set @> m.event_seq::bigint", ""},
		{"insert", "INSERT INTO message(id) SELECT read_set FROM " + relation, ""},
		{"unscoped", strings.ReplaceAll(query, "m.organization_id = sqlc.arg(organization_id)", "true"), "missing scope: m.organization_id"},
		{"scope_ON", "SELECT m.id FROM " + relation + " JOIN message m ON m.organization_id = $1", "missing scope: m.organization_id"},
		{"second_unscoped", query + " AND EXISTS (SELECT 1 FROM message n)", "missing scope: n.organization_id"},
		{"unknown", "SELECT * FROM " + relation + ", absent", "unsupported shape: relation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"message": "organization_id"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestReadSetArrayArguments(t *testing.T) {
	for _, argument := range []string{
		"sqlc.arg(x)::int4multirange[]", "sqlc.arg(x)::int8range[]", "sqlc.arg(x)::tsrange[]", "sqlc.arg(x)::nummultirange[]",
		"sqlc.arg(x)::int8multirange", "sqlc.narg(x)::int8multirange[]", "sqlc.arg(x)::int8multirange(1)[]",
		"sqlc.arg(x)::int8multirange[2]", "sqlc.arg(x)::int8multirange[][]", "sqlc.arg(x)::int8multirange[2][]",
		"sqlc.arg(x)::pg_catalog.int8multirange[]", "sqlc.arg(x)::public.int8multirange[]",
		"x::int8multirange[]", "'{}'::int8multirange[]", "ARRAY[sqlc.arg(x)::int8multirange]", "(SELECT x FROM message)::int8multirange[]",
		"lower(sqlc.arg(x))::int8multirange[]", "sqlc.arg(x)::uuid[], sqlc.arg(y)::int8multirange[]",
		"sqlc.arg(x + 1)::int8multirange[]", "sqlc.arg(m.x)::int8multirange[]", "$1::int8multirange[]",
	} {
		t.Run(argument, func(t *testing.T) {
			sql := "SELECT * FROM unnest(" + argument + ") WITH ORDINALITY AS c(read_set, ord)"
			err := check(sql, map[string]string{"message": "organization_id"})
			if err == nil || !strings.Contains(err.Error(), "unsupported unnest") {
				t.Fatalf("want argument refusal, got %v", err)
			}
		})
	}
	for _, relation := range []string{
		"unnest(sqlc.arg(x)::int8multirange[]) AS c(read_set)",
		"unnest(sqlc.arg(x)::uuid[], sqlc.arg(y)::int8multirange[]) AS c(id, read_set)",
		"pg_catalog.unnest(sqlc.arg(x)::int8multirange[]) WITH ORDINALITY AS c(read_set, ord)",
		"unnest(sqlc.arg(x)::int8multirange[]) WITH ORDINALITY AS c(read_set)",
		"LATERAL unnest(sqlc.arg(x)::int8multirange[]) WITH ORDINALITY AS c(read_set, ord)",
		"ROWS FROM(unnest(sqlc.arg(x)::int8multirange[])) WITH ORDINALITY AS c(read_set, ord)",
		"unnest(sqlc.arg(x)::int8multirange[]) WITH ORDINALITY AS c(read_set int8multirange, ord bigint)",
	} {
		t.Run(relation, func(t *testing.T) {
			sql := "SELECT * FROM " + relation
			err := check(sql, map[string]string{"message": "organization_id"})
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("want relation refusal, got %v", err)
			}
		})
	}
}

func TestReadSetArrayUnionScope(t *testing.T) {
	relation := "unnest(sqlc.arg(read_sets)::int8multirange[]) WITH ORDINALITY AS c(read_set, ord)"
	message := "SELECT m.id FROM message m WHERE m.organization_id = sqlc.arg(organization_id)"
	cte := "WITH x AS (SELECT c.read_set FROM " + relation + ") "
	uuid := "WITH x AS (SELECT a.channel_id FROM unnest(sqlc.arg(channel_ids)::uuid[]) WITH ORDINALITY AS a(channel_id, ord)) "
	exists := message + " AND EXISTS (SELECT 1 FROM " + relation + ", x WHERE c.read_set @> "
	for _, tc := range []struct{ name, sql, want string }{
		{"branch_CTE_multirange", "(" + cte + exists + "x.read_set)) UNION ALL " + message, ""},
		{"branch_CTE_uuid", "(" + uuid + exists + "x.channel_id)) UNION ALL " + message, ""},
		{"branch_CTE_scalar_left", "(" + cte + message + " AND EXISTS (SELECT 1 FROM x WHERE sqlc.arg(single_read_set)::int8multirange @> x.read_set)) UNION ALL " + message, ""},
		{"branch_CTE_derived", "(" + cte + "SELECT d.id FROM (SELECT m.id FROM " + relation + " JOIN x ON true JOIN message m ON true WHERE m.organization_id = sqlc.arg(organization_id) AND NOT (c.read_set @> x.read_set)) d) UNION ALL " + message, ""},
		{"branch_plain_table", cte + "SELECT m.id FROM x JOIN message m ON true WHERE m.organization_id = sqlc.arg(organization_id) AND NOT (sqlc.arg(single_read_set)::int8multirange @> m.event_seq) UNION ALL " + message, ""},
		{"branch_missing_scope", cte + "SELECT m.id FROM x JOIN message m ON true WHERE sqlc.arg(single_read_set)::int8multirange @> m.event_seq UNION ALL " + message, "missing scope: m.organization_id"},
		{"branch_plain_table_without_array", message + " AND NOT (sqlc.arg(single_read_set)::int8multirange @> m.event_seq) UNION ALL " + message, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"message": "organization_id"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
