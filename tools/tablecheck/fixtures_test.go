package tablecheck

import (
	"strings"
	"testing"
)

func TestOwnershipFixtures(t *testing.T) {
	owners := map[string]string{"message": "conversation", "account": "identity"}
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
	for _, reason := range []string{"", " ", "PENDING MAINTAINER: read", "pending_maintainer", " Pending--MAINTAINER: read", "pending/maintainer: read"} {
		if err := checkExemptions(map[exemption]bool{{"Q", "account", reason}: true}, false); err == nil {
			t.Fatalf("accepted %q", reason)
		}
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
	} {
		if _, _, err := ownership(source); err == nil {
			t.Fatalf("accepted manifest: %s", source)
		}
	}
}
