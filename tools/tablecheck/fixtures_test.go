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
		{"qualified custom operator", "SELECT 1 OPERATOR(public.###) 2", "", "unsupported qualified operator"},
		{"qualified builtin operator", "SELECT 1 OPERATOR(pg_catalog.+) 2", "", "unsupported qualified operator"},
		{"unknown operator", "SELECT 1 ### 2", "", "unsupported operator"},
		{"unused builtin operator", "SELECT 1 - 2", "", "unsupported operator"},
		{"allowed operators", "SELECT 1 = 2, 1 < 2, 1 > 2, 1 <= 2, 1 + 2", "", ""},
		{"qualified custom type", "SELECT '1'::public.mytype", "", "unsupported type"},
		{"unknown type", "SELECT '1'::mytype", "", "unsupported type"},
		{"qualified builtin type", "SELECT '1'::pg_catalog.uuid", "", "unsupported type"},
		{"explicit parser type", "SELECT '1'::pg_catalog.int8", "", "unsupported type"},
		{"unused builtin type", "SELECT '1'::int8", "", "unsupported type"},
		{"qualified quoted parser type", `SELECT '1'::"pg_catalog" /* comment */ . "int8"`, "", "unsupported type"},
		{"allowed types", "SELECT '1'::bigint, '1'::BIGINT, '{}'::jsonb, '{}'::uuid[]", "", ""},
		{"allowed cast still checks argument", "SELECT (SELECT id FROM account)::bigint", "", "write=false"},
		{"locking", "SELECT * FROM message FOR UPDATE", "", ""},
		{"locking alias", "SELECT * FROM message m FOR UPDATE OF m", "", "unsupported FOR UPDATE OF"},
		{"exemption table scope", "SELECT * FROM organization", "Reviewed read", "foreign table organization"},
		{"stale", "SELECT * FROM message", "Reviewed read", "stale"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allow := map[exemption]bool{}
			if tt.reason != "" {
				allow[exemption{"conversation.Q", "account", tt.reason}] = false
			}
			err := checkSQL(tt.sql, "conversation", "conversation.Q", owners, allow)
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
		if err := checkExemptions(map[exemption]bool{{"conversation.Q", "account", reason}: true}, false); err == nil {
			t.Fatalf("accepted %q", reason)
		}
	}
	for _, reason := range []string{"Reviewed read", "Maintainer approved #637", "Maintainer approved decision 26", "Maintainer approved https://github.com/tkakkie/ribbitto/pull/658#discussion_r123"} {
		if err := checkExemptions(map[exemption]bool{{"conversation.Q", "account", reason}: true}, false); err != nil {
			t.Fatalf("rejected %q: %v", reason, err)
		}
	}
	if err := checkExemptions(map[exemption]bool{{"conversation.Q", "account", "First reason"}: true, {"conversation.Q", "account", "Second reason"}: true}, false); err == nil {
		t.Fatal("accepted duplicate exemption")
	}
}

func TestExemptionQueryScope(t *testing.T) {
	allow := map[exemption]bool{{"conversation.Q", "account", "Reviewed read"}: false}
	err := checkSQL("SELECT * FROM account", "conversation", "conversation.R", map[string]string{"account": "identity"}, allow)
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
		{"DO $$ BEGIN EXECUTE 'CREATE TABLE orphan(id int)'; END $$;", "unsupported migration DO block"},
		{"DO $$ BEGIN NULL; END $$;", "unsupported migration DO block"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $body$ DO $$ BEGIN CREATE/**/TABLE orphan(id int); END $$; $body$;", "unsupported migration DO block"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN CREATE TABLE orphan(id int); END $$;", "unsupported migration routine"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN eXeCuTe 'SELECT 1'; END $$;", "unsupported migration routine"},
		{"CREATE PROCEDURE f() LANGUAGE plpgsql AS $$ BEGIN cReAtE tAbLe orphan(id int); END $$;", "unsupported migration routine"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql AS 'ALTER TABLE account RENAME TO renamed';", "unsupported migration routine"},
		{"CREATE PROCEDURE f() LANGUAGE sql AS 'drop table account';", "unsupported migration routine"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql AS 'CREATE/**/TEMPORARY TABLE orphan(id int)';", "unsupported migration routine"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql AS E'CREA\\x54E TABLE orphan(id int)';", "unsupported migration routine"},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql BEGIN ATOMIC CREATE TABLE orphan(id int); END;", "unsupported migration routine"},
		{"CREATE PROCEDURE f() LANGUAGE sql BEGIN ATOMIC ALTER TABLE account RENAME TO renamed; END;", "unsupported migration routine"},
		{"CREATE TABLE account(id int); CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM account; RETURN NULL; END $$;", ""},
		{"CREATE TABLE account(id int); CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END;", ""},
	} {
		err := checkMigrations([]string{"-- +goose Up\n" + tt.sql + "\n-- +goose Down\nDROP TABLE account;"}, map[string]string{"account": "identity"})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Fatalf("got %v, want %q", err, tt.want)
		}
	}
}

func TestReadExemptionFormat(t *testing.T) {
	for _, text := range []string{
		"conversation/test.sql:Q account Reviewed read",
		"conversation.Q account",
		"conversation.Q account Pending Maintainer: #637",
		"conversation.Q account Maintainer approved",
		"conversation.Q account Reviewed read\nconversation.Q account Reviewed read",
		"conversation.Q account First reason\nconversation.Q account Second reason",
	} {
		if _, err := readExemptions(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	allow, err := readExemptions("conversation.Q account Reviewed read")
	if err != nil || len(allow) != 1 {
		t.Fatalf("exemptions: %v, %v", allow, err)
	}
	if err := checkSQL("SELECT * FROM account", "conversation", "conversation.Q", map[string]string{"account": "identity"}, allow); err != nil {
		t.Fatal(err)
	}
	if err := checkExemptions(allow, true); err != nil {
		t.Fatal(err)
	}
}
