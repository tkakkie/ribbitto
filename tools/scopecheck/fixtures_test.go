package scopecheck

import (
	"strings"
	"testing"
)

func TestMutations(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"scoped", "SELECT * FROM member WHERE organization_id = $1", ""},
		{"missing", "SELECT * FROM member", "missing scope"},
		{"OR", "SELECT * FROM member WHERE organization_id = $1 OR true", "missing scope"},
		{"wrong_alias", "SELECT m.* FROM member m, organization o WHERE o.id = $1", "missing scope"},
		{"subquery_only", "SELECT * FROM member m WHERE EXISTS (SELECT 1 FROM member n WHERE n.organization_id = $1)", "missing scope"},
		{"inner_join", "SELECT m.* FROM member m JOIN organization o ON m.organization_id = o.id WHERE m.organization_id = $1 AND o.id = $1", ""},
		{"join_ON", "SELECT m.* FROM member m JOIN organization o ON m.organization_id = o.id AND m.organization_id = $1 AND o.id = $1", "missing scope"},
		{"outer_join", "SELECT m.* FROM member m LEFT JOIN organization o ON m.organization_id = o.id AND o.id = $1", "unsupported shape"},
		{"scoped_outer_join", "SELECT m.* FROM member m LEFT JOIN organization o ON m.organization_id = o.id WHERE m.organization_id = $1 AND o.id = $1", "unsupported shape"},
		{"CTE_DELETE", "WITH d AS (DELETE FROM member RETURNING *) SELECT 1", "missing scope"},
		{"scoped_CTE_DELETE", "WITH d AS (DELETE FROM member WHERE organization_id = $1 RETURNING *) SELECT 1", ""},
		{"insert_CTE_DELETE", "WITH d AS (DELETE FROM member RETURNING organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM d", "missing scope"},
		{"sqlc_arg", "SELECT * FROM member m WHERE m.organization_id = sqlc.arg(organization_id)", ""},
		{"quoted_arg", "UPDATE member SET handle = $2 WHERE organization_id = sqlc.arg('organization_id')", ""},
		{"literal", "SELECT * FROM member WHERE organization_id = 'abc'", "missing scope"},
		{"quoted_function", `SELECT * FROM member WHERE organization_id = "sqlc.arg"('organization_id')`, "missing scope"},
		{"quoted_column", `SELECT * FROM member m WHERE "m.organization_id" = $1`, "missing scope"},
		{"unqualified_join", "SELECT m.* FROM member m JOIN organization o ON m.organization_id = o.id WHERE organization_id = $1 AND o.id = $1", "missing scope"},
		{"update_FROM", "UPDATE member m SET handle = $2 FROM organization o WHERE m.organization_id = $1", "missing scope"},
		{"delete_USING", "DELETE FROM member m USING organization o WHERE m.organization_id = $1 AND o.id = $1", ""},
		{"nested_unscoped", "SELECT (SELECT count(*) FROM member) FROM organization WHERE id = $1", "missing scope"},
		{"unsupported", "MERGE INTO member USING organization ON true WHEN MATCHED THEN DELETE", "unsupported shape"},
		{"union", "SELECT * FROM member WHERE organization_id = $1 UNION SELECT * FROM member", "unsupported shape"},
		{"CTE_result", "WITH d AS (SELECT 1) SELECT * FROM d", "unsupported shape"},
		{"CTE_shadow", "WITH member AS (SELECT 1) SELECT 1", "unsupported shape"},
		{"range_subselect", "SELECT * FROM (SELECT * FROM member WHERE organization_id = $1) m", "unsupported shape"},
		{"renamed_columns", "SELECT * FROM member m(organization_id, id) WHERE m.organization_id = $1", "unsupported shape"},
		{"global", "SELECT * FROM setup", ""},
		{"insert", "INSERT INTO member (organization_id) VALUES ($1)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"member": "organization_id", "organization": "id", "setup": ""})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAllowlist(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Existing Resolves scope through a join.", ""},
		{"Removed Old exemption.", "stale allowlist entry"},
		{"Existing", "invalid allowlist entry"},
		{"Existing One reason.\nExisting Another reason.", "invalid allowlist entry"},
	} {
		_, err := exemptions(tc.text, map[string]string{"Existing": "SELECT 1"})
		if tc.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}
