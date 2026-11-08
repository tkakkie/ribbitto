package tablecheck

import (
	"strings"
	"testing"
)

func TestReadSetArrayExpressions(t *testing.T) {
	relation := "unnest(sqlc.arg(read_sets)::int8multirange[]) WITH ORDINALITY AS c(read_set, ord)"
	joined := "unnest(sqlc.arg(channel_ids)::uuid[]) WITH ORDINALITY AS a(channel_id, ord) JOIN unnest(sqlc.arg(positions)::bigint[]) WITH ORDINALITY AS b(p, ord) ON b.ord = a.ord JOIN " + relation + " ON c.ord = a.ord JOIN message m ON m.channel_id = a.channel_id"
	query := "SELECT m.id FROM " + joined + " WHERE m.organization_id = sqlc.arg(organization_id) AND NOT (c.read_set @> m.event_seq::bigint)"
	for _, tc := range []struct{ name, sql, want string }{
		{"ordinal_join", query, ""},
		{"right_table", strings.ReplaceAll(query, "m.event_seq::bigint", "m.event_seq"), ""},
		{"right_unnest_cast", strings.ReplaceAll(query, "m.event_seq::bigint", "b.p::bigint"), ""},
		{"right_unqualified_without_array", "SELECT sqlc.arg(x)::int8multirange @> event_seq FROM message", ""},
		{"right_self", strings.ReplaceAll(query, "m.event_seq::bigint", "c.read_set"), "unsupported containment"},
		{"right_unqualified", strings.ReplaceAll(query, "m.event_seq::bigint", "read_set"), "unsupported containment"},
		{"right_uuid", strings.ReplaceAll(query, "m.event_seq::bigint", "a.channel_id"), "unsupported containment"},
		{"right_bigint_unnest", strings.ReplaceAll(query, "m.event_seq::bigint", "b.p"), "unsupported containment"},
		{"right_scalar_left", strings.ReplaceAll(query, "c.read_set @> m.event_seq::bigint", "sqlc.arg(x)::int8multirange @> c.read_set"), "unsupported containment"},
		{"right_second_array", "SELECT c.read_set @> d.read_set FROM " + relation + ", " + strings.ReplaceAll(relation, "AS c(", "AS d("), "unsupported containment"},
		{"right_derived", "SELECT c.read_set @> x.read_set FROM " + relation + ", (SELECT * FROM " + relation + ") x", "unsupported containment"},
		{"right_CTE", "WITH x AS (SELECT * FROM " + relation + ") SELECT sqlc.arg(x)::int8multirange @> x.read_set FROM x", "unsupported containment"},
		{"right_correlated", "SELECT (SELECT sqlc.arg(x)::int8multirange @> m.event_seq FROM " + relation + ") FROM message m", "unsupported containment"},
		{"right_join_alias", "SELECT c.read_set @> j.event_seq FROM " + relation + ", (message m JOIN message n ON true) j", "unsupported containment"},
		{"right_duplicate_table", "SELECT c.read_set @> m.event_seq FROM " + relation + ", message m, message m", "unsupported containment"},
		{"aliased_join_clash", "SELECT 1 FROM (message c JOIN message m ON c.read_set @> m.event_seq) j, " + relation, "unsupported containment"},
		{"aliased_join_clash_cast", "SELECT 1 FROM (message c JOIN message m ON c.read_set @> sqlc.arg(seq)::bigint) j, " + relation, "unsupported containment"},
		{"implicit_unnest_clash", "SELECT unnest.unnest @> 1::bigint FROM unnest(sqlc.arg(ids)::uuid[]), " + strings.ReplaceAll(relation, "c(read_set, ord)", "unnest(unnest, ord)"), "unsupported containment"},
		{"array_inside_aliased_join", "SELECT 1 FROM (" + relation + " JOIN message m ON true) j", "unsupported type int8multirange"},
		{"only_relation", "SELECT c.read_set @> sqlc.arg(seq)::bigint FROM " + relation, ""},
		{"string_name", "SELECT c.read_set @> sqlc.arg(seq)::bigint FROM " + strings.ReplaceAll(relation, "arg(read_sets)", "arg('read_sets')"), ""},
		{"expression", strings.ReplaceAll(query, "m.event_seq::bigint", "(m.event_seq + 1)::bigint"), ""},
		{"CTE", "WITH x AS (" + query + ") SELECT * FROM x", ""},
		{"derived", "SELECT * FROM (" + query + ") x", ""},
		{"update", "UPDATE message m SET body='x' FROM " + relation + " WHERE m.organization_id=$1 AND c.read_set @> m.event_seq::bigint", ""},
		{"insert", "INSERT INTO message(id) SELECT read_set FROM " + relation, ""},
		{"union_unnest_unchanged", query + " UNION ALL " + query, "unsupported function unnest"},
		{"uuid_left", strings.ReplaceAll(query, "c.read_set @>", "a.channel_id @>"), "unsupported containment"},
		{"bigint_left", strings.ReplaceAll(query, "c.read_set @>", "b.p @>"), "unsupported containment"},
		{"ordinal_left", strings.ReplaceAll(query, "c.read_set @>", "c.ord @>"), "unsupported containment"},
		{"table_left", strings.ReplaceAll(query, "c.read_set @>", "m.read_set @>"), "unsupported containment"},
		{"unqualified_left", strings.ReplaceAll(query, "c.read_set @>", "read_set @>"), "unsupported containment"},
		{"qualified_left", strings.ReplaceAll(query, "c.read_set @>", "public.c.read_set @>"), "unsupported containment"},
		{"duplicate_columns", strings.ReplaceAll(query, "c(read_set, ord)", "c(read_set, read_set)"), "unsupported containment"},
		{"duplicate_alias", "SELECT c.read_set @> m.event_seq FROM " + relation + ", message c, message m", "unsupported containment"},
		{"sibling", "SELECT (SELECT c.read_set @> 1::bigint FROM message c), (SELECT * FROM " + relation + ")", "unsupported containment"},
		{"shadowed", "SELECT (SELECT c.read_set @> 1::bigint FROM message c) FROM " + relation, "unsupported containment"},
		{"correlated", "SELECT (SELECT c.read_set @> 1::bigint) FROM " + relation, "unsupported containment"},
		{"outer", "SELECT c.read_set @> 1::bigint FROM (SELECT * FROM " + relation + ") c", "unsupported containment"},
		{"CTE_output", "WITH c AS (SELECT * FROM " + relation + ") SELECT c.read_set @> 1::bigint FROM c", "unsupported containment"},
		{"union_sibling", query + " UNION ALL SELECT m.id FROM message m WHERE m.read_set @> 1::bigint", "unsupported function unnest"},
		{"foreign_read", strings.ReplaceAll(query, "JOIN message m", "JOIN account m"), "foreign table account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSQL(tc.sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, expr := range []string{
		"c.read_set <@ 1::bigint", "c.read_set && c.read_set", "c.read_set -|- c.read_set",
		"c.read_set OPERATOR(pg_catalog.@>) 1::bigint",
		"int8range(1, 2)", "int8multirange()", "range_agg(c.read_set)", "range_intersect_agg(c.read_set)",
		"c.read_set @> sqlc.arg(seq)::uuid", "c.read_set @> 1::int", "c.read_set @> '1'::text",
		"c.read_set @> sqlc.arg(seq)::int8range", "c.read_set @> sqlc.arg(seq)::bigint[]",
		"c.read_set @> sqlc.arg(seq)::pg_catalog.int8", "c.read_set @> hidden_reader()::bigint",
		"c.read_set @> sqlc.arg(seq)", "c.read_set @> m.*",
		"sqlc.arg(read_sets)::int8multirange[]", // A nearby permitted array must not allow this cast.
	} {
		t.Run(expr, func(t *testing.T) {
			err := checkSQL("SELECT "+expr+" FROM "+joined, "conversation", "conversation.Q", map[string]string{"message": "conversation"}, nil)
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("want expression refusal, got %v", err)
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
			err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation"}, nil)
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
			err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation"}, nil)
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("want relation refusal, got %v", err)
			}
		})
	}
}
