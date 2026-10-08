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
		{"distinct", "SELECT * FROM member WHERE organization_id IS DISTINCT FROM $1", "missing scope"},
		{"not_equal", "SELECT * FROM member WHERE organization_id <> $1", "missing scope"},
		{"update_target", "UPDATE member SET handle = $1", "missing scope"},
		{"using_unscoped", "DELETE FROM member m USING member n WHERE m.organization_id = $1", "missing scope"},
		{"qualified_table", "SELECT * FROM public.member WHERE organization_id = $1", "unsupported shape"},
		{"literal", "SELECT * FROM member WHERE organization_id = 'abc'", "missing scope"},
		{"quoted_function", `SELECT * FROM member WHERE organization_id = "sqlc.arg"('organization_id')`, "missing scope"},
		{"quoted_column", `SELECT * FROM member m WHERE "m.organization_id" = $1`, "missing scope"},
		{"unqualified_join", "SELECT m.* FROM member m JOIN organization o ON m.organization_id = o.id WHERE organization_id = $1 AND o.id = $1", "missing scope"},
		{"update_FROM", "UPDATE member m SET handle = $2 FROM organization o WHERE m.organization_id = $1", "missing scope"},
		{"delete_USING", "DELETE FROM member m USING organization o WHERE m.organization_id = $1 AND o.id = $1", ""},
		{"nested_unscoped", "SELECT (SELECT count(*) FROM member) FROM organization WHERE id = $1", "missing scope"},
		{"unsupported", "MERGE INTO member USING organization ON true WHEN MATCHED THEN DELETE", "unsupported shape"},
		{"union", "SELECT * FROM member WHERE organization_id = $1 UNION SELECT * FROM member", "unsupported shape"},
		{"CTE_result", "WITH d AS (SELECT 1) SELECT * FROM d", ""},
		{"CTE_read_unscoped", "WITH d AS (SELECT * FROM member) SELECT * FROM d", "missing scope"},
		{"CTE_DELETE_read", "WITH d AS (DELETE FROM member RETURNING id) SELECT count(*) FROM d", "missing scope"},
		{"CTE_shadow_read", "WITH member AS (SELECT 1) SELECT * FROM member", "unsupported shape"},
		{"CTE_shadow", "WITH member AS (SELECT 1) SELECT 1", "unsupported shape"},
		{"range_subselect", "SELECT * FROM (SELECT * FROM member WHERE organization_id = $1) m", "unsupported shape"},
		{"renamed_columns", "SELECT * FROM member m(organization_id, id) WHERE m.organization_id = $1", "unsupported shape"},
		{"global", "SELECT * FROM setup", ""},
		{"insert", "INSERT INTO member (organization_id) VALUES ($1)", ""},
		{"insert_returning", "INSERT INTO member (organization_id) VALUES ($1) RETURNING *", ""},
		{"insert_VALUES_subquery", "INSERT INTO message (organization_id, body) VALUES ($1, (SELECT body FROM message WHERE organization_id = $1))", "unsupported shape"},
		{"insert_RETURNING_subquery", "INSERT INTO message (organization_id, body) VALUES ($1, $2) RETURNING (SELECT body FROM message WHERE organization_id = $1 LIMIT 1)", "unsupported shape"},
		{"CTE_insert_VALUES_subquery", "WITH i AS (INSERT INTO message (organization_id, body) VALUES ($1, (SELECT body FROM message WHERE organization_id = $1)) RETURNING *) SELECT * FROM i", "unsupported shape"},
		{"insert_SELECT", "INSERT INTO member (organization_id) SELECT organization_id FROM member", "unsupported shape"},
		{"insert_SELECT_CTE", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM d", ""},
		{"insert_SELECT_CTE_RETURNING_subquery", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO message (organization_id) SELECT organization_id FROM d RETURNING (SELECT body FROM message WHERE organization_id = $1 LIMIT 1)", "unsupported shape"},
		{"insert_SELECT_CTE_union", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO message (organization_id) SELECT organization_id FROM d UNION SELECT organization_id FROM message WHERE organization_id = $1", "unsupported shape"},
		{"insert_SELECT_CTE_qualified", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM public.d", "unsupported shape"},
		{"insert_SELECT_scoped_table", "INSERT INTO member (organization_id) SELECT organization_id FROM member WHERE organization_id = $1", "unsupported shape"},
		{"insert_SELECT_CTE_join", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT d.organization_id FROM d JOIN member m ON true WHERE m.organization_id = $1", "unsupported shape"},
		{"insert_SELECT_CTE_subquery", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM d WHERE organization_id IN (SELECT organization_id FROM member WHERE organization_id = $1)", "unsupported shape"},
		{"insert_SELECT_CTE_scalar", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT (SELECT organization_id FROM member WHERE organization_id = $1) FROM d", "unsupported shape"},
		{"insert_SELECT_CTE_derived", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM (SELECT * FROM d) x", "unsupported shape"},
		{"insert_SELECT_CTE_function", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT d.organization_id FROM d, generate_series(1, 2)", "unsupported shape"},
		{"insert_SELECT_CTE_only_join", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT a.organization_id FROM d a JOIN d b ON true", "unsupported shape"},
		{"insert_SELECT_CTE_unscoped", "WITH d AS (SELECT * FROM member) INSERT INTO member (organization_id) SELECT organization_id FROM d", "missing scope"},
		{"insert_SELECT_other_statement_CTE", "WITH d AS (SELECT $1 AS organization_id) SELECT * FROM d; INSERT INTO member (organization_id) SELECT organization_id FROM d", "unsupported shape"},
		{"insert_SELECT_CTE_conflict_update", "WITH d AS (SELECT $1 AS organization_id) INSERT INTO member (organization_id) SELECT organization_id FROM d ON CONFLICT (id) DO UPDATE SET organization_id = $1", "unsupported shape"},
		{"CTE_insert_SELECT", "WITH i AS (INSERT INTO member (organization_id) SELECT organization_id FROM member RETURNING *) SELECT 1", "unsupported shape"},
		{"insert_conflict_update", "INSERT INTO member (organization_id) VALUES ($1) ON CONFLICT (id) DO UPDATE SET organization_id = $1", "unsupported shape"},
		{"CTE_insert_conflict_update", "WITH i AS (INSERT INTO member (organization_id) VALUES ($1) ON CONFLICT (id) DO UPDATE SET organization_id = $1 RETURNING *) SELECT 1", "unsupported shape"},
		{"insert_conflict_nothing", "INSERT INTO member (organization_id) VALUES ($1) ON CONFLICT DO NOTHING", "unsupported shape"},
		{"insert_default", "INSERT INTO member DEFAULT VALUES", "unsupported shape"},
		{"insert_override", "INSERT INTO member (organization_id) OVERRIDING SYSTEM VALUE VALUES ($1)", "unsupported shape"},
		{"insert_ordered_values", "INSERT INTO member (organization_id) VALUES ($1) ORDER BY 1", "unsupported shape"},
		{"CTE_insert_values", "WITH i AS (INSERT INTO member (organization_id) VALUES ($1) RETURNING *) SELECT * FROM i", ""},
		{"insert_CTE_unscoped_read", "WITH d AS (SELECT * FROM member) INSERT INTO member (organization_id) VALUES ($1)", "missing scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.sql, map[string]string{"member": "organization_id", "message": "organization_id", "organization": "id", "setup": ""})
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
		{"", ""},
		{" \n\n", ""},
		{"Scoped Unneeded exemption.", "unnecessary allowlist entry"},
		{"Existing Resolves scope through a join.", ""},
		{"Existing Approved by maintainer, 2026-10-08.", "missing maintainer provenance"},
		{"Existing Approved by Maintainer.", "missing maintainer provenance"},
		{"Existing Approved by MAINTAINER in #658, 2026-10-08.", ""},
		{"Existing Approved by maintainer in https://github.com/tkakkie/ribbitto/pull/658#issuecomment-6049254742.", ""},
		{"Existing Approved by maintainer in decision 21.", ""},
		{"Existing Approved by maintainer in #0.", "missing maintainer provenance"},
		{"Existing Approved by maintainer in https://github.com/tkakkie/ribbitto/pull/658.", "missing maintainer provenance"},
		{"Existing PENDING MAINTAINER: shape rejected by #658; decision needed.", "pending maintainer approval"},
		{"Existing PENDING MAINTAINER: decision needed.", "pending maintainer approval"},
		{"Existing   Pending Maintainer: decision needed, #658.", "pending maintainer approval"},
		{"Insert Pending shape exemption.", ""},
		{"Removed Old exemption.", "stale allowlist entry"},
		{"Existing", "invalid allowlist entry"},
		{"Existing One reason.\nExisting Another reason.", "invalid allowlist entry"},
	} {
		_, err := exemptions(tc.text, map[string]string{"Existing": "SELECT * FROM member", "Scoped": "SELECT 1", "Insert": "INSERT INTO member (organization_id) SELECT organization_id FROM member"}, map[string]string{"member": "organization_id"})
		if tc.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}

func TestSchema(t *testing.T) {
	for _, tc := range []struct{ sql, table, column, want string }{
		{"CREATE TABLE member (organization_id uuid)", "member", "organization_id", ""},
		{"CREATE TABLE member (id uuid); ALTER TABLE member ADD COLUMN organization_id uuid", "member", "organization_id", ""},
		{"CREATE TABLE member (id uuid)", "", "", "unknown ownership: member"},
		{"CREATE TABLE pin (id uuid, org uuid REFERENCES organization(id))", "", "", "unknown ownership: pin"},
		{"CREATE TABLE organization (id uuid)", "organization", "id", ""},
		{"CREATE TABLE account (id uuid)", "account", "", ""},
		{"CREATE TABLE session (account_id uuid)", "session", "", ""},
		{"CREATE TABLE setup (id boolean)", "setup", "", ""},
		{"CREATE TABLE setup (organization_id uuid)", "setup", "", ""},
		{"CREATE TABLE member (organization_id uuid); -- +goose Down\nDROP TABLE member", "member", "organization_id", ""},
		{"CREATE TABLE setup (id boolean); DROP INDEX member_idx; ALTER INDEX other RENAME TO renamed", "setup", "", ""},
		{"ALTER TABLE member RENAME COLUMN organization_id TO other", "", "", "unsupported schema shape"},
		{"ALTER TABLE member RENAME TO other", "", "", "unsupported schema shape"},
		{"DROP TABLE member", "", "", "unsupported schema shape"},
		{"CREATE TABLE member (LIKE other)", "", "", "unsupported schema shape"},
		{"CREATE TABLE member (id uuid) INHERITS (other)", "", "", "unsupported schema shape"},
		{"ALTER TABLE member INHERIT other", "", "", "unsupported schema shape"},
		{"CREATE TABLE member OF other", "", "", "unsupported schema shape"},
		{"ALTER TABLE member OF other", "", "", "unsupported schema shape"},
		{"CREATE TABLE public.member (organization_id uuid)", "", "", "unsupported schema shape"},
		{"ALTER TABLE public.member ADD COLUMN organization_id uuid", "", "", "unsupported schema shape"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			var migrations []string
			for _, table := range []string{"account", "session", "setup"} {
				if table != tc.table {
					migrations = append(migrations, "CREATE TABLE "+table+" (id uuid)")
				}
			}
			tables, err := schema(append(migrations, tc.sql))
			if tc.want == "" {
				column, exists := tables[tc.table]
				if err != nil || !exists || column != tc.column {
					t.Fatalf("want %s scope %q, got %v, %v", tc.table, tc.column, tables, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestInstallationWide(t *testing.T) {
	base := "CREATE TABLE account (id uuid); CREATE TABLE session (id uuid); CREATE TABLE setup (organization_id uuid);"
	for _, tc := range []struct{ name, sql, want string }{
		{"scoped_account", strings.Replace(base, "account (id uuid)", "account (organization_id uuid)", 1), "listed installation-wide but scoped: account"},
		{"scoped_session", base + "ALTER TABLE session ADD COLUMN organization_id uuid", "listed installation-wide but scoped: session"},
		{"stale_account", strings.Replace(base, "CREATE TABLE account (id uuid);", "", 1), "stale installation-wide entry: account"},
		{"stale_session", strings.Replace(base, "CREATE TABLE session (id uuid);", "", 1), "stale installation-wide entry: session"},
		{"stale_setup", strings.Replace(base, "CREATE TABLE setup (organization_id uuid);", "", 1), "stale installation-wide entry: setup"},
		{"alter_without_create", strings.Replace(base, "CREATE TABLE setup (organization_id uuid);", "ALTER TABLE setup ADD COLUMN organization_id uuid;", 1), "stale installation-wide entry: setup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schema([]string{tc.sql})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
