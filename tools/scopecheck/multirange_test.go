package scopecheck

import (
	"strings"
	"testing"
)

func TestReadSetScope(t *testing.T) {
	filter := "NOT (sqlc.arg(read_set)::int8multirange @> m.event_seq)"
	tables := "message m JOIN member n ON true"
	for _, tc := range []struct{ name, sql, want string }{
		{"scoped", "SELECT m.id FROM " + tables + " WHERE m.organization_id = $1 AND n.organization_id = $1 AND " + filter, ""},
		{"first_unscoped", "SELECT m.id FROM " + tables + " WHERE n.organization_id = $1 AND " + filter, "missing scope: m.organization_id"},
		{"second_unscoped", "SELECT m.id FROM " + tables + " WHERE m.organization_id = $1 AND " + filter, "missing scope: n.organization_id"},
		{"CTE", "WITH c AS (SELECT * FROM message m WHERE m.organization_id = $1 AND " + filter + ") SELECT * FROM c", ""},
		{"unscoped_CTE", "WITH c AS (SELECT * FROM message m WHERE " + filter + ") SELECT * FROM c", "missing scope: m.organization_id"},
		{"unknown_table", "SELECT * FROM absent m WHERE m.organization_id = $1 AND " + filter, "unsupported shape: relation"},
		{"unknown_statement", "CREATE TABLE x AS SELECT * FROM message m WHERE m.organization_id = $1 AND " + filter, "unsupported shape: CreateTableAsStmt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"message": "organization_id", "member": "organization_id"})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
