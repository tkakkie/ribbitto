package tablecheck

import (
	"strings"
	"testing"
)

func TestOwnershipFixtures(t *testing.T) {
	owners := map[string]string{"message": "conversation", "account": "identity", "organization": "org"}
	for _, tt := range []struct{ name, sql, reason, want string }{
		{"owned insert", "INSERT INTO message VALUES (1)", "", ""},
		{"owned update", "UPDATE message SET id=1", "", ""},
		{"owned delete CTE", "WITH gone AS (DELETE FROM message RETURNING *) SELECT * FROM gone", "", ""},
		{"foreign insert", "INSERT INTO account VALUES (1)", "", "write=true"},
		{"foreign update", "UPDATE account SET id=1", "", "write=true"},
		{"foreign delete", "DELETE FROM account", "", "write=true"},
		{"foreign CTE insert", "WITH x AS (INSERT INTO account VALUES (1) RETURNING *) SELECT * FROM x", "", "write=true"},
		{"foreign CTE delete", "WITH x AS (DELETE FROM account RETURNING *) SELECT * FROM x", "", "write=true"},
		{"subquery read", "SELECT * FROM message WHERE EXISTS (SELECT 1 FROM account)", "", "write=false"},
		{"exempt subquery", "SELECT * FROM (SELECT * FROM account) x", "Reviewed read", ""},
		{"exempt read still forbids write", "UPDATE account SET id=(SELECT id FROM account)", "Reviewed read", "write=true"},
		{"CTE name", "WITH x AS (SELECT * FROM message) SELECT * FROM x", "", ""},
		{"CTE body", "WITH x AS (SELECT * FROM account) SELECT * FROM x", "", "write=false"},
		{"CTE shadowing", "WITH account AS (SELECT * FROM account) SELECT * FROM account", "", "write=false"},
		{"CTE cannot hide write", "WITH account AS (SELECT 1) DELETE FROM account", "", "write=true"},
		{"CTE scope", "SELECT * FROM (WITH account AS (SELECT 1) SELECT * FROM account) x, account", "", "write=false"},
		{"unknown table", "SELECT * FROM absent", "", "unknown table"},
		{"unknown write", "DELETE FROM absent", "", "unknown table"},
		{"unsupported", "TRUNCATE message", "", "unsupported SQL node"},
		{"select into", "SELECT * INTO account FROM message", "", "unsupported SELECT INTO"},
		{"recursive", "WITH RECURSIVE x AS (SELECT 1) SELECT * FROM x", "", "unsupported recursive"},
		{"qualified", "SELECT * FROM other.message", "", "unsupported qualified"},
		{"several statements", "SELECT 1; DELETE FROM account", "", "expected one statement"},
		{"qualified write", "UPDATE other.message SET id=1", "", "unsupported qualified write"},
		{"function writer", "SELECT my_writer()", "", "unsupported function"},
		{"function SQL", "SELECT query_to_xml('select * from account', true, false, '')", "", "unsupported function"},
		{"function sequence", "SELECT nextval('account_id_seq')", "", "unsupported function"},
		{"qualified function", "SELECT public.lower('x')", "", "unsupported function"},
		{"quoted dotted function", `SELECT "sqlc.arg"()`, "", "unsupported function"},
		{"allowed functions", "SELECT sqlc.arg(id), sqlc.narg(id), count(*), max(id), lower(body) FROM message", "", ""},
		{"locking", "SELECT * FROM message FOR UPDATE", "", ""},
		{"locking alias", "SELECT * FROM message m FOR UPDATE OF m", "", "unsupported FOR UPDATE OF"},
		{"exemption table scope", "SELECT * FROM organization", "Reviewed read", "foreign table organization"},
		{"stale", "SELECT * FROM message", "Reviewed read", "stale"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allow := map[exemption]bool{}
			if tt.reason != "" {
				allow[exemption{"Q", "account", tt.reason}] = false
			}
			err := checkSQL(tt.sql, "conversation", "Q", owners, allow)
			if err == nil {
				err = checkExemptions(allow, true)
			}
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExemptionReasons(t *testing.T) {
	for _, reason := range []string{"", " ", "PENDING MAINTAINER: read", "pending_maintainer", " Pending--MAINTAINER: read", "pending/maintainer: read", "Reviewed read; PENDING MAINTAINER: confirm #637", "Maintainer approved"} {
		if err := checkExemptions(map[exemption]bool{{"Q", "account", reason}: true}, false); err == nil {
			t.Fatalf("accepted %q", reason)
		}
	}
	for _, reason := range []string{"Reviewed read", "Maintainer approved #637", "Maintainer approved decision 26", "Maintainer approved https://github.com/tkakkie/ribbitto/pull/658#discussion_r123"} {
		if err := checkExemptions(map[exemption]bool{{"Q", "account", reason}: true}, false); err != nil {
			t.Fatalf("rejected %q: %v", reason, err)
		}
	}
	if err := checkExemptions(map[exemption]bool{{"Q", "account", "First reason"}: true, {"Q", "account", "Second reason"}: true}, false); err == nil {
		t.Fatal("accepted duplicate exemption")
	}
}

func TestExemptionQueryScope(t *testing.T) {
	allow := map[exemption]bool{{"Q", "account", "Reviewed read"}: false}
	err := checkSQL("SELECT * FROM account", "conversation", "R", map[string]string{"account": "identity"}, allow)
	if err == nil || !strings.Contains(err.Error(), "foreign table account") {
		t.Fatalf("exemption leaked to another query: %v", err)
	}
}

func TestManifestFixtures(t *testing.T) {
	literal := `package p; func moduleManifest() []module { return []module{{root: "internal/identity", ownsTables: []string{"account"}}} }`
	owners, modules, err := ownership(literal)
	if err != nil || owners["account"] != "identity" || !modules["identity"] {
		t.Fatalf("ownership: %v, %v, %v", owners, modules, err)
	}
	for _, source := range []string{
		strings.ReplaceAll(literal, `ownsTables: []string{"account"}`, ""),
		strings.ReplaceAll(literal, `"account"`, `"account", "account"`),
		strings.ReplaceAll(literal, `[]string{"account"}`, "tables()"),
		strings.ReplaceAll(literal, "return []module", "sideEffect(); return []module"),
		strings.ReplaceAll(literal, `"internal/identity"`, "root()"),
		strings.ReplaceAll(literal, `root: "internal/identity",`, ""),
		strings.ReplaceAll(literal, `"internal/identity"`, `"identity"`),
		strings.ReplaceAll(literal, `"internal/identity"`, `"internal/nested/identity"`),
		strings.ReplaceAll(literal, `[]module{{`, `[]module{{root: "internal/identity", ownsTables: []string{}}, {`),
	} {
		if _, _, err := ownership(source); err == nil {
			t.Fatalf("accepted manifest: %s", source)
		}
	}
}

func TestQueryFixtures(t *testing.T) {
	for _, tt := range []struct{ sql, want string }{
		{"SELECT 1", "no named queries"},
		{"-- name: Q :one\nSELECT 1;\n-- name: Q :one\nSELECT 2;", "duplicate query"},
		{"SELECT 1;\n-- name: Q :one\nSELECT 2;", "SQL before first query"},
		{"-- name: Q :one\nSELECT 1;\n-- name: R :one\nSELECT 2;", ""},
	} {
		err := checkQueries(tt.sql, "conversation/test.sql", "conversation", nil, nil)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Fatalf("got %v, want %q", err, tt.want)
		}
	}
}

func TestMigrationFixtures(t *testing.T) {
	for _, tt := range []struct{ sql, want string }{
		{"CREATE TABLE account(id int);", ""},
		{"CREATE TABLE account(id int); CREATE TABLE orphan(id int);", "migration table has no owner"},
		{"SELECT 1;", "owned table not created"},
		{"ALTER TABLE account RENAME TO renamed;", "unsupported migration table"},
		{"DROP TABLE account;", "unsupported migration table"},
		{"CREATE TABLE account AS SELECT 1;", "unsupported migration table"},
		{"CREATE TABLE public.account(id int);", "unsupported qualified"},
		{"CREATE TABLE account(id int); CREATE TABLE account(id int);", "duplicate migration table"},
	} {
		err := checkMigrations([]string{"-- +goose Up\n" + tt.sql + "\n-- +goose Down\nDROP TABLE account;"}, map[string]string{"account": "identity"})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Fatalf("got %v, want %q", err, tt.want)
		}
	}
}
