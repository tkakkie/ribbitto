package scopecheck

import (
	"strings"
	"testing"
)

func TestDerivedReads(t *testing.T) {
	for _, shape := range []struct{ name, prefix, suffix string }{
		{"derived", "SELECT * FROM (", ") d"},
		{"cross_lateral", "SELECT * FROM unnest(sqlc.arg(ids)::uuid[]) a(id) CROSS JOIN LATERAL (", ") d"},
		{"inner_lateral", "SELECT * FROM unnest(sqlc.arg(ids)::uuid[]) a(id) JOIN LATERAL (", ") d ON true"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			for _, tc := range []struct{ name, body, want string }{
				{"scoped", "SELECT m.id FROM message m WHERE m.organization_id = $1 ORDER BY m.event_seq LIMIT 100", ""},
				{"unscoped", "SELECT m.id FROM message m ORDER BY m.event_seq LIMIT 100", "missing scope: m.organization_id"},
				{"two_tables", "SELECT m.id FROM message m JOIN member n ON true WHERE m.organization_id = $1 AND n.organization_id = $1", ""},
				{"first_unscoped", "SELECT m.id FROM message m JOIN member n ON true WHERE n.organization_id = $1", "missing scope: m.organization_id"},
				{"second_unscoped", "SELECT m.id FROM message m JOIN member n ON true WHERE m.organization_id = $1", "missing scope: n.organization_id"},
				{"first_ON_only", "SELECT m.id FROM message m JOIN member n ON m.organization_id = $1 WHERE n.organization_id = $1", "missing scope: m.organization_id"},
				{"second_ON_only", "SELECT m.id FROM message m JOIN member n ON n.organization_id = $1 WHERE m.organization_id = $1", "missing scope: n.organization_id"},
				{"nested_scoped", "SELECT * FROM (SELECT * FROM message m WHERE m.organization_id = $1) x", ""},
				{"anonymous", "SELECT * FROM (SELECT * FROM message m WHERE m.organization_id = $1)", ""},
				{"anonymous_unscoped", "SELECT * FROM (SELECT * FROM message m)", "missing scope: m.organization_id"},
				{"output_aliases", "SELECT * FROM (SELECT m.id FROM message m WHERE m.organization_id = $1) x(id)", ""},
				{"nested_unscoped", "SELECT * FROM (SELECT * FROM message m) x", "missing scope: m.organization_id"},
				{"CTE_scoped", "WITH x AS (SELECT * FROM message m WHERE m.organization_id = $1) SELECT * FROM x", ""},
				{"CTE_unscoped", "WITH x AS (SELECT * FROM message m) SELECT * FROM x", "missing scope: m.organization_id"},
				{"shadow_table", "SELECT * FROM (SELECT 1) message", "derived alias shadows table"},
				{"ordinality", "SELECT * FROM unnest(sqlc.arg(ids)::uuid[]) WITH ORDINALITY a(id, n)", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					sql := shape.prefix + tc.body + shape.suffix
					err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
					if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
						t.Fatalf("%s: want %q, got %v", sql, tc.want, err)
					}
				})
			}
		})
	}
}

func TestDerivedOuterScope(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"correlated", "SELECT * FROM member n CROSS JOIN LATERAL (SELECT * FROM message m WHERE m.organization_id = $1 AND m.id = n.id ORDER BY m.event_seq LIMIT 100) d WHERE n.organization_id = $1", ""},
		{"outer_missing", "SELECT * FROM member n CROSS JOIN LATERAL (SELECT * FROM message m WHERE m.organization_id = $1 AND m.id = n.id) d", "missing scope: n.organization_id"},
		{"inner_missing", "SELECT * FROM member n CROSS JOIN LATERAL (SELECT * FROM message m WHERE m.id = n.id) d WHERE n.organization_id = $1", "missing scope: m.organization_id"},
		{"inner_scope_outside", "SELECT * FROM (SELECT * FROM message m) d WHERE d.organization_id = $1", "missing scope: m.organization_id"},
		{"shadow_outer_table", "SELECT * FROM message m, (SELECT 1) message WHERE m.organization_id = $1", "derived alias shadows table"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"message": "organization_id", "member": "organization_id"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDerivedRefusals(t *testing.T) {
	for _, join := range []string{"LEFT", "RIGHT", "FULL"} {
		for _, lateral := range []string{"", "LATERAL "} {
			t.Run(join+"/"+strings.TrimSpace(lateral), func(t *testing.T) {
				sql := "SELECT * FROM member n " + join + " JOIN " + lateral + "(SELECT * FROM message m WHERE m.organization_id = $1) d ON true WHERE n.organization_id = $1"
				err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
				if err == nil || !strings.Contains(err.Error(), "unsupported shape: join") {
					t.Fatalf("want join refusal, got %v", err)
				}
			})
		}
	}
	for _, tc := range []struct{ name, relation, want string }{
		{"lateral_function", "LATERAL lower('a') a", "unsupported shape: LATERAL function"},
		{"function_ordinality", "lower('a') WITH ORDINALITY a(value, n)", "unsupported shape: function WITH ORDINALITY"},
		{"rows_function", "ROWS FROM(lower('a')) a", "unsupported shape: ROWS FROM"},
		{"lateral_unnest", "LATERAL unnest(sqlc.arg(ids)::uuid[]) a(id)", "unsupported unnest relation shape"},
		{"lateral_unnest_ordinality", "LATERAL unnest(sqlc.arg(ids)::uuid[]) WITH ORDINALITY a(id, n)", "unsupported unnest relation shape"},
		{"rows_unnest", "ROWS FROM(unnest(sqlc.arg(ids)::uuid[])) a(id)", "unsupported unnest relation shape"},
		{"multi_ordinality", "unnest(sqlc.arg(ids)::uuid[], sqlc.arg(lo)::bigint[]) WITH ORDINALITY a(id, n)", "unsupported unnest ordinality: requires one argument"},
		{"unnest_ordinality_alias", "unnest(sqlc.arg(ids)::uuid[]) WITH ORDINALITY a(id)", "unsupported unnest ordinality: requires alias with two columns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Nest the refusal in a permitted derived read, so no outer refusal hides it.
			err := check("SELECT * FROM (SELECT * FROM "+tc.relation+") d", map[string]string{"message": "organization_id"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	for _, lateral := range []string{"", "LATERAL "} {
		for _, tc := range []struct{ name, prefix, suffix, want string }{
			{"insert", "INSERT INTO message(id) SELECT id FROM ", "", "unsupported shape: INSERT SELECT derived source"},
			{"update", "UPDATE message m SET id=d.id FROM ", " WHERE m.organization_id = $1", "unsupported shape: UpdateStmt derived source"},
			{"delete", "DELETE FROM message m USING ", " WHERE m.organization_id = $1", "unsupported shape: DeleteStmt derived source"},
		} {
			t.Run(tc.name+"/"+strings.TrimSpace(lateral), func(t *testing.T) {
				sql := tc.prefix + lateral + "(SELECT id FROM member n WHERE n.organization_id = $1) d" + tc.suffix
				err := check(sql, map[string]string{"message": "organization_id", "member": "organization_id"})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			})
		}
	}
}
