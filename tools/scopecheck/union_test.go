package scopecheck

import (
	"strings"
	"testing"
)

func TestUnionAllReads(t *testing.T) {
	first := "SELECT m.id FROM message m JOIN member n ON true WHERE m.organization_id = $1 AND n.organization_id = $1"
	second := "SELECT p.id FROM message p JOIN member q ON true WHERE p.organization_id = $1 AND q.organization_id = $1"
	union := first + " UNION ALL " + second
	for _, shape := range []struct{ name, prefix, suffix string }{
		{"top", "", ""},
		{"derived", "SELECT * FROM (", ") d ORDER BY d.id LIMIT 100"},
		{"cross_lateral", "SELECT * FROM unnest(sqlc.arg(ids)::uuid[]) a(id) CROSS JOIN LATERAL (", ") d"},
		{"inner_lateral", "SELECT * FROM unnest(sqlc.arg(ids)::uuid[]) a(id) JOIN LATERAL (", ") d ON true"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			t.Run("scoped", func(t *testing.T) { unionCheck(t, shape.prefix+union+shape.suffix, "") })
			t.Run("same_alias", func(t *testing.T) { unionCheck(t, shape.prefix+first+" UNION ALL "+first+shape.suffix, "") })
			missing := strings.Replace(first, "m.organization_id = $1", "true", 1)
			t.Run("same_alias_first_missing", func(t *testing.T) {
				unionCheck(t, shape.prefix+missing+" UNION ALL "+first+shape.suffix, "missing scope: m.organization_id")
			})
			t.Run("same_alias_second_missing", func(t *testing.T) {
				unionCheck(t, shape.prefix+first+" UNION ALL "+missing+shape.suffix, "missing scope: m.organization_id")
			})
			for _, alias := range []string{"m", "n", "p", "q"} {
				// Each fixture removes only one table's predicate; the other three stay scoped.
				predicate := alias + ".organization_id = $1"
				t.Run(alias+"_missing", func(t *testing.T) {
					sql := strings.Replace(union, predicate, "true", 1)
					unionCheck(t, shape.prefix+sql+shape.suffix, "missing scope: "+alias+".organization_id")
				})
				t.Run(alias+"_ON_only", func(t *testing.T) {
					branch, other := first, second
					if alias == "p" || alias == "q" {
						branch, other = second, first
					}
					branch = strings.Replace(branch, predicate, "true", 1)
					branch = strings.Replace(branch, "ON true", "ON "+predicate, 1)
					unionCheck(t, shape.prefix+branch+" UNION ALL "+other+shape.suffix, "missing scope: "+alias+".organization_id")
				})
			}
		})
	}
	for _, tc := range []struct{ name, sql, want string }{
		{"nested", "(" + union + ") UNION ALL SELECT * FROM unnest(sqlc.arg(ids)::uuid[])", ""},
		{"nested_missing", "(" + union + ") UNION ALL SELECT id FROM message r", "missing scope: r.organization_id"},
		{"outer_scope_only", "SELECT * FROM (SELECT organization_id FROM message UNION ALL SELECT organization_id FROM member WHERE organization_id=$1) d WHERE d.organization_id=$1", "missing scope: message.organization_id"},
		{"unknown_branch", first + " UNION ALL SELECT id FROM unknown_table", `unsupported shape: relation "unknown_table"`},
		{"values_branch", "SELECT 1 UNION ALL VALUES (2)", "unsupported shape: SELECT operation"},
	} {
		t.Run(tc.name, func(t *testing.T) { unionCheck(t, tc.sql, tc.want) })
	}
}

func TestUnionReadRefusals(t *testing.T) {
	for _, op := range []string{"UNION", "INTERSECT", "INTERSECT ALL", "EXCEPT", "EXCEPT ALL"} {
		for _, shape := range []struct{ name, prefix, suffix string }{
			{"top", "", ""},
			{"derived", "SELECT * FROM (", ") d"},
			{"nested", "SELECT 1 UNION ALL (", ")"},
		} {
			t.Run(op+"/"+shape.name, func(t *testing.T) {
				sql := "SELECT id FROM message WHERE organization_id=$1 " + op + " SELECT id FROM member WHERE organization_id=$1"
				want := "unsupported shape: SELECT " + strings.Fields(op)[0]
				if op == "UNION" {
					want += " (distinct)"
				}
				unionCheck(t, shape.prefix+sql+shape.suffix, want)
			})
		}
	}
}

func TestUnionWriteSources(t *testing.T) {
	for _, tc := range []struct{ name, statement, derived, want string }{
		{"insert", "INSERT INTO message(id) SELECT id FROM d", "INSERT INTO message(id) ", "InsertStmt"},
		{"update", "UPDATE message m SET id=d.id FROM d WHERE m.organization_id=$1", "UPDATE message m SET id=d.id FROM (", "UpdateStmt"},
		{"delete", "DELETE FROM message m USING d WHERE m.organization_id=$1", "DELETE FROM message m USING (", "DeleteStmt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			branch := "SELECT id FROM member WHERE organization_id=$1"
			// The single SELECT control proves the CTE source has no independent refusal.
			unionCheck(t, "WITH d AS ("+branch+") "+tc.statement, "")
			for _, op := range []string{"UNION ALL", "UNION", "INTERSECT", "INTERSECT ALL", "EXCEPT", "EXCEPT ALL"} {
				t.Run(op, func(t *testing.T) {
					source := branch + " " + op + " " + branch
					want := "unsupported shape: " + tc.want + " set operation source"
					t.Run("CTE", func(t *testing.T) { unionCheck(t, "WITH d AS ("+source+") "+tc.statement, want) })
					t.Run("CTE_write", func(t *testing.T) {
						unionCheck(t, "WITH d AS ("+source+"), changed AS ("+tc.statement+" RETURNING *) SELECT * FROM changed", want)
					})
					direct := tc.derived + source
					if tc.name != "insert" {
						direct += ") d WHERE m.organization_id=$1"
					}
					t.Run("direct", func(t *testing.T) { unionCheck(t, direct, want) })
				})
			}
		})
	}
}

func unionCheck(t *testing.T, sql, want string) {
	t.Helper()
	err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
	if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("%s: want %q, got %v", sql, want, err)
	}
}
