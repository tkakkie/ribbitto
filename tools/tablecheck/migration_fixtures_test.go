package tablecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationObjects(t *testing.T) {
	owners := map[string]string{"message": "conversation", "account": "identity", "organization": "org", "member": "org"}
	prefix := "CREATE TABLE message(id int); CREATE TABLE account(id int); CREATE TABLE organization(id int); CREATE TABLE member(id int); "
	bind := "CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION f();"
	routine := func(body string) string {
		return "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ " + body + " $$; " + bind
	}
	for _, tt := range []struct{ name, sql, want string }{
		{"index", "CREATE INDEX ix ON message(id)", ""},
		{"sequence", "CREATE SEQUENCE ids", ""},
		{"add column", "ALTER TABLE message ADD COLUMN extra int", ""},
		{"set not null", "ALTER TABLE message ALTER COLUMN id SET NOT NULL", ""},
		{"add constraint", "ALTER TABLE message ADD CONSTRAINT c CHECK(id > 0)", ""},
		{"update backfill", "UPDATE message SET id=(SELECT id FROM message)", ""},
		{"insert backfill", "INSERT INTO message SELECT id FROM message", ""},
		{"backfill foreign read", "INSERT INTO message SELECT id FROM account", "foreign table account (write=false, owner=identity)"},
		{"backfill CTE foreign write", "WITH x AS (DELETE FROM account RETURNING id) UPDATE message SET id=1", "foreign table account (write=true"},
		{"backfill unknown owner", "UPDATE absent SET id=1", "unknown backfill owner"},
		{"rule", "CREATE RULE r AS ON INSERT TO message DO ALSO INSERT INTO account VALUES(1)", "RuleStmt"},
		{"view", "CREATE VIEW v AS SELECT * FROM account", "ViewStmt"},
		{"materialized view", "CREATE MATERIALIZED VIEW v AS SELECT * FROM account", "CreateTableAsStmt"},
		{"policy", "CREATE POLICY p ON message USING (true)", "CreatePolicyStmt"},
		{"procedure", "CREATE PROCEDURE f() LANGUAGE sql AS 'SELECT 1'", "unsupported migration procedure"},
		{"do", "DO $$ BEGIN NULL; END $$", "DoStmt"},
		{"execute", "EXECUTE p", "ExecuteStmt"},
		{"call procedure", "CALL p()", "CallStmt"},
		{"foreign table", "CREATE FOREIGN TABLE remote(id int) SERVER s", "CreateForeignTableStmt"},
		{"select into", "SELECT * INTO copied FROM account", "SelectStmt"},
		{"create as", "CREATE TABLE copied AS SELECT * FROM account", "CreateTableAsStmt"},
		{"select", "SELECT 1", "SelectStmt"},
		{"delete", "DELETE FROM message", "DeleteStmt"},
		{"truncate", "TRUNCATE message", "TruncateStmt"},
		{"copy", "COPY message FROM STDIN", "CopyStmt"},
		{"set", "SET search_path TO public", "VariableSetStmt"},
		{"drop", "DROP TABLE message", "DropStmt"},
		{"rename", "ALTER TABLE message RENAME TO renamed", "RenameStmt"},
		{"drop column", "ALTER TABLE message DROP COLUMN id", "AT_DropColumn"},
		{"set default", "ALTER TABLE message ALTER COLUMN id SET DEFAULT 0", "AT_ColumnDefault"},
		{"alter type", "ALTER TABLE message ALTER COLUMN id TYPE bigint", "AT_AlterColumnType"},
		{"alter sequence", "ALTER SEQUENCE ids RESTART", "AlterSeqStmt"},
		{"alter index", "ALTER INDEX ix SET (fillfactor=50)", "unsupported migration ALTER object"},
		{"same module", routine("BEGIN UPDATE message SET id=1; RETURN NEW; END"), ""},
		{"org access epoch", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE organization SET access_epoch=access_epoch+1 WHERE id=NEW.organization_id; RETURN NEW; END $$; CREATE TRIGGER t AFTER INSERT ON member FOR EACH ROW EXECUTE FUNCTION f();", ""},
		{"foreign insert", routine("BEGIN INSERT INTO account VALUES(1); RETURN NULL; END"), "foreign table account (write=true"},
		{"foreign update", routine("BEGIN UPDATE account SET id=1; RETURN NULL; END"), "foreign table account (write=true"},
		{"foreign delete", routine("BEGIN DELETE FROM account; RETURN NULL; END"), "foreign table account (write=true"},
		{"foreign select", routine("BEGIN SELECT id FROM account; RETURN NULL; END"), "foreign table account (write=false"},
		{"foreign perform", routine("BEGIN PERFORM id FROM account; RETURN NULL; END"), "foreign table account (write=false"},
		{"account only bound to message", routine("BEGIN PERFORM id FROM account; RETURN NULL; END"), "foreign table account"},
		{"if subquery", routine("BEGIN IF EXISTS(SELECT FROM account) THEN RETURN NULL; END IF; RETURN NEW; END"), "foreign table account"},
		{"elsif subquery", routine("BEGIN IF false THEN RETURN NULL; ELSIF EXISTS(SELECT FROM account) THEN RETURN NULL; END IF; RETURN NEW; END"), "foreign table account"},
		{"declaration subquery", routine("DECLARE x int := (SELECT id FROM account); BEGIN RETURN NULL; END"), "foreign table account"},
		{"assignment subquery", routine("DECLARE x int; BEGIN x := (SELECT id FROM account); RETURN NULL; END"), "foreign table account"},
		{"record assignment", routine("BEGIN NEW.id := 1; RETURN NEW; END"), ""},
		{"assignment equals", routine("DECLARE x int; BEGIN x = (SELECT id FROM account); RETURN NULL; END"), "foreign table account"},
		{"assignment target", routine("DECLARE x int[]; BEGIN x[(SELECT id FROM account)] := 1; RETURN NULL; END"), "unsupported migration routine assignment target"},
		{"raise expression", routine("BEGIN RAISE EXCEPTION 'x' USING DETAIL=(SELECT id FROM account); RETURN NULL; END"), "foreign table account"},
		{"return expression", routine("BEGIN RETURN (SELECT id FROM account); END"), "foreign table account"},
		{"select into variable", routine("DECLARE x int; BEGIN SELECT id INTO x FROM message; RETURN NULL; END"), ""},
		{"foreign select into variable", routine("DECLARE x int; BEGIN SELECT id INTO x FROM account; RETURN NULL; END"), "foreign table account"},
		{"pl select into table", routine("BEGIN SELECT id INTO copied FROM account; RETURN NULL; END"), "not a known variable"},
		{"routine procedure call", routine("BEGIN CALL p(); RETURN NULL; END"), "PLpgSQL_stmt_call"},
		{"routine foreign table", routine("BEGIN CREATE FOREIGN TABLE remote(id int) SERVER s; RETURN NULL; END"), "CreateForeignTableStmt"},
		{"routine do", routine("BEGIN DO 'BEGIN NULL; END'; RETURN NULL; END"), "PLpgSQL_stmt_call"},
		{"dynamic", routine("BEGIN EXECUTE 'SELECT 1'; RETURN NULL; END"), "PLpgSQL_stmt_dynexecute"},
		{"dynamic loop", routine("DECLARE x record; BEGIN FOR x IN EXECUTE 'SELECT 1' LOOP NULL; END LOOP; RETURN NULL; END"), "PLpgSQL_stmt_dynfors"},
		{"dynamic cursor", routine("DECLARE c refcursor; BEGIN OPEN c FOR EXECUTE 'SELECT 1'; RETURN NULL; END"), "PLpgSQL_stmt_open"},
		{"invalid routine", routine("BEGIN syntax error; END"), "parsing migration routine"},
		{"invalid embedded SQL", routine("BEGIN PERFORM FROM; RETURN NULL; END"), "syntax error"},
		{"unsupported SQL expression", routine("BEGIN PERFORM query_to_xml('SELECT * FROM account',true,false,''); RETURN NULL; END"), "unsupported migration function"},
		{"unsupported operator", routine("BEGIN PERFORM 1 ### 2; RETURN NULL; END"), "unsupported migration operator"},
		{"sql same module", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT id FROM message; SELECT 1'; " + bind, ""},
		{"sql invalid", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT FROM'; " + bind, "syntax error"},
		{"sql foreign", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT id FROM account'; " + bind, "foreign table account"},
		{"sql select into owned name", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT id INTO message FROM message'; " + bind, "unsupported SELECT INTO"},
		{"sql select into", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT id INTO copied FROM account'; " + bind, "unsupported SELECT INTO"},
		{"sql inline same module", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql BEGIN ATOMIC SELECT id FROM message; END; " + bind, ""},
		{"sql inline foreign", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql BEGIN ATOMIC SELECT id FROM account; END; " + bind, "foreign table account"},
		{"sql return foreign", "CREATE FUNCTION f() RETURNS trigger LANGUAGE sql RETURN (SELECT id FROM account); " + bind, "foreign table account"},
		{"missing pl body", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql RETURN NULL; " + bind, "unsupported migration routine language or body"},
		{"other language", "CREATE FUNCTION f() RETURNS trigger LANGUAGE c AS 'lib', 'f'; " + bind, "unsupported migration routine body"},
		{"unbound", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$", "unbound migration function f"},
		{"ambiguous bindings", routine("BEGIN RETURN NULL; END") + " CREATE TRIGGER t2 AFTER INSERT ON account FOR EACH ROW EXECUTE FUNCTION f()", "ambiguous trigger function owner f"},
		{"same owner bindings", routine("BEGIN RETURN NULL; END") + " CREATE TRIGGER t2 AFTER UPDATE ON message FOR EACH ROW EXECUTE FUNCTION f()", ""},
		{"missing binding", "CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION absent()", "unsupported migration trigger binding"},
		{"qualified binding", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER t AFTER INSERT ON public.message FOR EACH ROW EXECUTE FUNCTION f()", "unsupported migration trigger binding"},
		{"qualified definition", "CREATE FUNCTION public.f() RETURNS trigger LANGUAGE sql AS 'SELECT 1'", "unsupported migration function definition"},
		{"duplicate definition", routine("BEGIN RETURN NULL; END") + " CREATE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT 1'", "unsupported migration function definition"},
		{"replacement", "CREATE OR REPLACE FUNCTION f() RETURNS trigger LANGUAGE sql AS 'SELECT 1'", "unsupported migration function definition"},
		{"builtin named call", "CREATE FUNCTION count() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER tc AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION count(); UPDATE message SET id=count()", "call to migration-defined function count"},
		{"call from backfill", routine("BEGIN RETURN NULL; END") + " UPDATE message SET id=f()", "call to migration-defined function f"},
		{"qualified call", routine("BEGIN RETURN NULL; END") + " INSERT INTO message VALUES(public.f())", "call to migration-defined function public.f"},
		{"forward call", "UPDATE message SET id=f(); " + routine("BEGIN RETURN NULL; END"), "call to migration-defined function f"},
		{"call from routine", routine("BEGIN PERFORM g(); RETURN NULL; END") + " CREATE FUNCTION g() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER tg AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION g()", "call to migration-defined function g"},
		{"call from default", routine("BEGIN RETURN NULL; END") + " ALTER TABLE message ADD COLUMN extra int DEFAULT f()", "call to migration-defined function f"},
		{"call from check", routine("BEGIN RETURN NULL; END") + " ALTER TABLE message ADD CONSTRAINT c CHECK(f() > 0)", "call to migration-defined function f"},
		{"call from create default", routine("BEGIN RETURN NULL; END"), "call to migration-defined function f"},
		{"call from create check", routine("BEGIN RETURN NULL; END"), "call to migration-defined function f"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tables := prefix
			if tt.name == "call from create default" {
				tables = strings.Replace(tables, "message(id int)", "message(id int DEFAULT f())", 1)
			}
			if tt.name == "call from create check" {
				tables = strings.Replace(tables, "message(id int)", "message(id int CHECK(f() > 0))", 1)
			}
			err := checkMigrations([]string{"-- +goose Up\n" + tables + tt.sql}, owners)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReviewedMigrationAccess(t *testing.T) {
	source, err := os.ReadFile("../../module_imports_test.go")
	if err != nil {
		t.Fatal(err)
	}
	owners, _, err := ownership(string(source))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	var original []migration
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name, _, _ := strings.Cut(filepath.Base(path), "_")
		original = append(original, migration{name, string(data)})
	}
	for _, tt := range []struct{ name, remove, target, old, new, want string }{
		{name: "production"},
		{name: "remove 00006", remove: "00006.statement2", want: "00006.statement2: foreign table organization (write=false, owner=org)"},
		{name: "remove 00007", remove: "00007.organization_event_seq_logged", want: "00007.organization_event_seq_logged: foreign table event_log (write=false, owner=realtime)"},
		{name: "00006 extra access", target: "00006", old: "SELECT o.id, 'general', true", new: "SELECT o.id, (SELECT email FROM account LIMIT 1), true", want: "00006.statement2: foreign table account (write=false, owner=identity)"},
		{name: "00007 extra access", target: "00007", old: "IF NEW.event_seq >", new: "IF EXISTS (SELECT FROM account) AND NEW.event_seq >", want: "00007.organization_event_seq_logged: foreign table account (write=false, owner=identity)"},
		{name: "00007 dynamic", target: "00007", old: "BEGIN", new: "BEGIN EXECUTE 'SELECT 1';", want: "00007.organization_event_seq_logged: unsupported migration routine node PLpgSQL_stmt_dynexecute (dynamic SQL is refused)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allow, err := migrationExemptions(reviewedMigrationAccess)
			if err != nil {
				t.Fatal(err)
			}
			for e := range allow {
				if e.object == tt.remove {
					delete(allow, e)
				}
			}
			files := append([]migration(nil), original...)
			for i, f := range files {
				if f.name == tt.target {
					if !strings.Contains(f.sql, tt.old) {
						t.Fatal("mutation target missing")
					}
					files[i].sql = strings.Replace(f.sql, tt.old, tt.new, 1)
				}
			}
			err = checkMigrationsWithAccess(files, owners, allow)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || err.Error() != tt.want) {
				t.Fatalf("got %v, want exactly %q", err, tt.want)
			}
		})
	}
}

func TestMigrationExemptions(t *testing.T) {
	for _, text := range []string{"x account read", "x account execute Reviewed", "x account read PENDING MAINTAINER: #677", "x account read Maintainer approved", "x account read First\nx account read Second"} {
		if _, err := migrationExemptions(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	owners := map[string]string{"message": "conversation", "account": "identity"}
	prefix := "CREATE TABLE message(id int); CREATE TABLE account(id int); "
	bind := " CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION f();"
	for _, tt := range []struct{ name, sql, allow, want string }{
		{"reviewed write", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO account VALUES(1); RETURN NULL; END $$;" + bind, "00001.f account write Maintainer ruling #677", ""},
		{"read does not exempt write", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO account VALUES(1); RETURN NULL; END $$;" + bind, "00001.f account read Reviewed", "write=true"},
		{"write does not exempt read", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM account; RETURN NULL; END $$;" + bind, "00001.f account write Reviewed", "write=false"},
		{"object scope", "UPDATE message SET id=(SELECT id FROM account)", "00001.other account read Reviewed", "foreign table account"},
		{"stale", "UPDATE message SET id=1", "00001.statement3 account read Reviewed", "stale migration exemption"},
		{"same module stale", "UPDATE message SET id=1", "00001.statement3 message write Reviewed", "stale migration exemption"},
		{"unknown owner cannot be exempted", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM account; RETURN NULL; END $$;", "00001.f account read Reviewed", "unbound migration function"},
		{"call cannot be exempted", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;" + bind + " UPDATE message SET id=f()", "00001.statement5 account read Reviewed", "call to migration-defined function"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allow, err := migrationExemptions(tt.allow)
			if err != nil {
				t.Fatal(err)
			}
			err = checkMigrationsWithAccess([]migration{{"00001", "-- +goose Up\n" + prefix + tt.sql}}, owners, allow)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
