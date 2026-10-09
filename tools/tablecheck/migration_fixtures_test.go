package tablecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migrationFixtureOwnership(t *testing.T) map[string]string {
	t.Helper()
	source, err := os.ReadFile("../../module_imports_test.go")
	if err != nil {
		t.Fatal(err)
	}
	owners, _, err := ownership(string(source))
	if err != nil {
		t.Fatal(err)
	}
	return owners
}

func TestMigrationForeignKeys(t *testing.T) {
	owners := migrationFixtureOwnership(t)
	for _, form := range []struct{ name, sql string }{
		{"create column", "CREATE TABLE message(id uuid, account_id uuid REFERENCES %s(id) %s)"},
		{"create table", "CREATE TABLE message(id uuid, account_id uuid, CONSTRAINT fk FOREIGN KEY(account_id) REFERENCES %s(id) %s)"},
		{"add constraint", "CREATE TABLE message(id uuid, account_id uuid); ALTER TABLE message ADD CONSTRAINT fk FOREIGN KEY(account_id) REFERENCES %s(id) %s"},
		{"add column", "CREATE TABLE message(id uuid); ALTER TABLE message ADD COLUMN account_id uuid REFERENCES %s(id) %s"},
	} {
		for _, reference := range []string{"account", "channel"} {
			for _, tt := range []struct{ action, want string }{
				{"ON DELETE CASCADE", "ON DELETE CASCADE"},
				{"ON DELETE SET NULL", "ON DELETE SET NULL"},
				{"ON DELETE SET DEFAULT", "ON DELETE SET DEFAULT"},
				{"ON UPDATE CASCADE", "ON UPDATE CASCADE"},
				{"ON UPDATE SET NULL", "ON UPDATE SET NULL"},
				{"ON UPDATE SET DEFAULT", "ON UPDATE SET DEFAULT"},
				{"ON DELETE RESTRICT", ""},
				{"ON UPDATE RESTRICT", ""},
				{"ON DELETE NO ACTION", ""},
				{"ON UPDATE NO ACTION", ""},
				{"", ""},
				{"ON DELETE RESTRICT ON UPDATE CASCADE", "ON UPDATE CASCADE"},
				{"ON DELETE SET NULL ON UPDATE NO ACTION", "ON DELETE SET NULL"},
				{"ON DELETE CASCADE ON UPDATE SET DEFAULT", "ON DELETE CASCADE"},
			} {
				t.Run(form.name+"/"+reference+"/"+tt.action, func(t *testing.T) {
					modules := map[string]string{"message": owners["message"], reference: owners[reference]}
					files := []string{"-- +goose Up\nCREATE TABLE " + reference + "(id uuid PRIMARY KEY);", "-- +goose Up\n" + fmt.Sprintf(form.sql, reference, tt.action)}
					err := checkMigrations(files, modules)
					if reference == "channel" || tt.want == "" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					want := "cross-module foreign key to account (conversation -> identity): " + tt.want + " writes referencing rows"
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("got %v, want %q", err, want)
					}
				})
			}
		}
	}
}

func TestMigrationForeignKeyTargets(t *testing.T) {
	manifest := migrationFixtureOwnership(t)
	for _, tt := range []struct{ name, reference, before, after, want string }{
		{"self", "message", "", "", ""},
		{"schema", "public.account", "CREATE TABLE account(id int);", "", "unsupported qualified migration foreign key target account"},
		{"catalog", "app.public.account", "CREATE TABLE account(id int);", "", "unsupported qualified migration foreign key target account"},
		{"unknown", "absent", "", "", "unknown migration foreign key target absent"},
		{"uncreated", "account", "", "", "owned table not created by migrations: account"},
		{"later", "account", "", "CREATE TABLE account(id int);", "unknown migration foreign key target account"},
		{"unowned", "absent", "CREATE TABLE absent(id int);", "", "migration table has no owner: absent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owners := map[string]string{"message": manifest["message"]}
			if tt.before != "" && tt.name != "unowned" || tt.name == "uncreated" || tt.name == "later" {
				owners["account"] = manifest["account"]
			}
			// Even a non-writing action must resolve; unknown ownership cannot
			// establish whether a referential action crosses a module boundary.
			for _, action := range []string{"", "ON DELETE RESTRICT", "ON DELETE CASCADE", "ON UPDATE SET NULL"} {
				sql := tt.before + "CREATE TABLE message(id int REFERENCES " + tt.reference + "(id) " + action + ");" + tt.after
				err := checkMigrations([]string{"-- +goose Up\n" + sql}, owners)
				if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
					t.Fatalf("%s: got %v, want %q", action, err, tt.want)
				}
			}
		})
	}
}

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
		{"security definer", strings.Replace(routine("BEGIN RETURN NULL; END"), "LANGUAGE plpgsql", "LANGUAGE plpgsql SECURITY DEFINER", 1), "function option security"},
		{"search path", strings.Replace(routine("BEGIN RETURN NULL; END"), "LANGUAGE plpgsql", "LANGUAGE plpgsql SET search_path = pg_temp", 1), "function option set"},
		{"support option", strings.Replace(routine("BEGIN RETURN NULL; END"), "LANGUAGE plpgsql", "LANGUAGE plpgsql SUPPORT helper", 1), "function option support"},
		{"volatility option", strings.Replace(routine("BEGIN RETURN NULL; END"), "LANGUAGE plpgsql", "LANGUAGE plpgsql IMMUTABLE", 1), "function option volatility"},
		{"exception block", routine("BEGIN RETURN NULL; EXCEPTION WHEN OTHERS THEN RETURN NULL; END"), "unsupported migration routine node PLpgSQL_exception_block"},
		{"case statement", routine("BEGIN CASE WHEN true THEN RETURN NULL; END CASE; RETURN NULL; END"), "unsupported migration routine node PLpgSQL_stmt_case"},
		{"assert statement", routine("BEGIN ASSERT true; RETURN NULL; END"), "unsupported migration routine node PLpgSQL_stmt_assert"},
		{"static for", routine("DECLARE x record; BEGIN FOR x IN SELECT id FROM message LOOP RETURN NULL; END LOOP; RETURN NULL; END"), "unsupported migration routine node PLpgSQL_stmt_fors"},
		{"diagnostics", routine("DECLARE x int; BEGIN GET DIAGNOSTICS x = ROW_COUNT; RETURN NULL; END"), "unsupported migration routine node PLpgSQL_stmt_getdiag"},
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
			if err != nil && strings.Contains(err.Error(), "(dynamic SQL is refused)") {
				t.Fatalf("misleading routine node error: %v", err)
			}
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMigrationAnnotations(t *testing.T) {
	for _, tt := range []struct{ name, sql string }{
		{"trailing comment marker", "-- +goose Up\nCREATE TABLE message(id int); -- +goose Down\nDROP TABLE message;\n-- +goose Down\n"},
		{"envsub", "-- +goose Up\n-- +goose ENVSUB ON\nCREATE TABLE message(id int);\n-- +goose Down\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMigrations([]string{tt.sql}, map[string]string{"message": "conversation"})
			if err == nil || !strings.Contains(err.Error(), "unsupported migration goose annotation") {
				t.Fatalf("got %v, want unsupported migration goose annotation", err)
			}
		})
	}
}

func TestMigrationDDLTargets(t *testing.T) {
	function := "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; "
	for _, tt := range []struct{ name, sql, want string }{
		{"schema index", "CREATE INDEX ix ON public.message(id)", "unsupported qualified migration IndexStmt target message"},
		{"catalog index", "CREATE INDEX ix ON app.public.message(id)", "unsupported qualified migration IndexStmt target message"},
		{"unknown index", "CREATE INDEX ix ON absent(id)", "unknown migration IndexStmt target absent"},
		{"uncreated index", "CREATE INDEX ix ON absent(id)", "unknown migration IndexStmt target absent"},
		{"schema alter", "ALTER TABLE public.message ADD COLUMN x int", "unsupported qualified migration AlterTableStmt target message"},
		{"catalog alter", "ALTER TABLE app.public.message ADD COLUMN x int", "unsupported qualified migration AlterTableStmt target message"},
		{"unknown alter", "ALTER TABLE absent ADD COLUMN x int", "unknown migration AlterTableStmt target absent"},
		{"uncreated alter", "ALTER TABLE absent ADD COLUMN x int", "unknown migration AlterTableStmt target absent"},
		{"schema trigger", function + "CREATE TRIGGER t AFTER INSERT ON public.message FOR EACH ROW EXECUTE FUNCTION f()", "unsupported migration trigger binding f"},
		{"catalog trigger", function + "CREATE TRIGGER t AFTER INSERT ON app.public.message FOR EACH ROW EXECUTE FUNCTION f()", "unsupported migration trigger binding f"},
		{"uncreated trigger", function + "CREATE TRIGGER t AFTER INSERT ON absent FOR EACH ROW EXECUTE FUNCTION f()", "unknown migration CreateTrigStmt target absent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owners := map[string]string{"message": "conversation"}
			if strings.HasPrefix(tt.name, "uncreated ") {
				owners["absent"] = "conversation"
			}
			err := checkMigrations([]string{"-- +goose Up\nCREATE TABLE message(id int); " + tt.sql}, owners)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMigrationDDLExpressions(t *testing.T) {
	owners := map[string]string{"message": "conversation", "account": "identity"}
	function := " CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; "
	for _, tt := range []struct{ name, sql, want string }{
		{"subquery", "CREATE TABLE message(id int); ALTER TABLE message ADD COLUMN x text DEFAULT (SELECT email FROM account LIMIT 1)", "00001.ddl.statement3: unsupported migration DDL node SubLink"},
		{"qualified operator", "CREATE TABLE message(id int DEFAULT (1 OPERATOR(public.###) 2))", "unsupported qualified migration DDL operator public.###"},
		{"unlisted operator", "CREATE TABLE message(id int CHECK (id ### 2))", "unsupported migration DDL operator ###"},
		{"unlisted cast", "CREATE TABLE message(id int DEFAULT ('1'::custom_type))", "unsupported migration DDL type custom_type"},
		{"qualified cast", "CREATE TABLE message(id int DEFAULT ('1'::public.custom_type))", "unsupported migration DDL type public.custom_type"},
		{"builtin cast", "CREATE TABLE message(id int DEFAULT ('1'::int))", "unsupported migration DDL type pg_catalog.int4"},
		{"unlisted node", "CREATE TABLE message(id int, x int GENERATED ALWAYS AS (CASE WHEN id > 0 THEN id ELSE 0 END) STORED)", "unsupported migration DDL node CaseExpr"},
		{"create default query", "CREATE TABLE message(id int, x xml DEFAULT query_to_xml('SELECT * FROM account', true, false, ''))", "DDL function query_to_xml"},
		{"create check query", "CREATE TABLE message(id int CHECK (query_to_xml('SELECT * FROM account', true, false, '') IS NOT NULL))", "DDL function query_to_xml"},
		{"create default table", "CREATE TABLE message(id int, x xml DEFAULT table_to_xml('account', true, false, ''))", "DDL function table_to_xml"},
		{"add default query", "CREATE TABLE message(id int); ALTER TABLE message ADD COLUMN x xml DEFAULT query_to_xml('SELECT * FROM account', true, false, '')", "DDL function query_to_xml"},
		{"add check table", "CREATE TABLE message(id int); ALTER TABLE message ADD CONSTRAINT c CHECK (table_to_xml('account', true, false, '') IS NOT NULL)", "DDL function table_to_xml"},
		{"trigger when query", "CREATE TABLE message(id int);" + function + "CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW WHEN (query_to_xml('SELECT * FROM account', true, false, '') IS NOT NULL) EXECUTE FUNCTION f()", "DDL function query_to_xml"},
		{"generated expression", "CREATE TABLE message(id int, x xml GENERATED ALWAYS AS (table_to_xml('account',true,false,'')) STORED)", "DDL function table_to_xml"},
		{"added generated expression", "CREATE TABLE message(id int); ALTER TABLE message ADD COLUMN x xml GENERATED ALWAYS AS (table_to_xml('account',true,false,'')) STORED", "DDL function table_to_xml"},
		{"index expression", "CREATE TABLE message(id int); CREATE INDEX ix ON message((table_to_xml('account',true,false,'')))", "DDL function table_to_xml"},
		{"index predicate", "CREATE TABLE message(id int); CREATE INDEX ix ON message(id) WHERE table_to_xml('account',true,false,'') IS NOT NULL", "DDL function table_to_xml"},
		{"index predicate operator", "CREATE TABLE message(id int); CREATE INDEX ix ON message(id) WHERE id ### 2", "unsupported migration DDL operator ###"},
		{"exclude predicate operator", "CREATE TABLE message(id int, EXCLUDE (id WITH =) WHERE (id ### 2))", "unsupported migration DDL operator ###"},
		{"trigger when operator", "CREATE TABLE message(id int);" + function + "CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW WHEN (NEW.id ### 2) EXECUTE FUNCTION f()", "unsupported migration DDL operator ###"},
		{"quoted dotted builtin", `CREATE TABLE message(s text CHECK (s="pg_catalog.normalize"(s)))`, "DDL function pg_catalog.normalize"},
		{"qualified builtin", "CREATE TABLE message(id int DEFAULT public.length('x'))", "DDL function public.length"},
		{"allowed functions", "CREATE TABLE message(id int, u uuid DEFAULT uuidv7(), t timestamptz DEFAULT now(), s text CHECK (length(s)>0 AND lower(s)=btrim(s) AND octet_length(s)>0 AND starts_with(s,'x') AND s=normalize(s,NFC))); CREATE INDEX ix ON message(lower(s)) WHERE length(s)>0", ""},
		{"inherits", "CREATE TABLE message(id int) INHERITS (account)", "CREATE TABLE inhRelations"},
		{"partition of", "CREATE TABLE message PARTITION OF account FOR VALUES IN (1)", "CREATE TABLE inhRelations"},
		{"partition spec", "CREATE TABLE message(id int) PARTITION BY LIST(id)", "CREATE TABLE partspec"},
		{"typed table", "CREATE TABLE message OF account", "CREATE TABLE ofTypename"},
		{"like", "CREATE TABLE message(LIKE account)", "CREATE TABLE LIKE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMigrations([]string{"-- +goose Up\nCREATE TABLE account(id int); " + tt.sql}, owners)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMigrationSequences(t *testing.T) {
	owners := map[string]string{"message": "conversation", "account": "identity"}
	prefix := "CREATE TABLE message(id bigint); CREATE TABLE account(id bigint); "
	owned := "CREATE SEQUENCE ids; ALTER TABLE message ADD COLUMN epoch bigint DEFAULT nextval('ids'); "
	bind := " CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION f();"
	for _, tt := range []struct{ name, setup, call, want string }{
		{"same module nextval", owned, "nextval('ids')", ""},
		{"same module currval", owned, "currval('ids')", ""},
		{"create default ownership", "CREATE SEQUENCE ids; CREATE TABLE extra(id bigint DEFAULT nextval('ids')); ", "nextval('ids')", ""},
		{"foreign sequence", "CREATE SEQUENCE ids; ALTER TABLE account ADD COLUMN epoch bigint DEFAULT nextval('ids'); ", "nextval('ids')", "foreign migration sequence ids"},
		{"uncreated", "", "nextval('absent')", "unknown or foreign migration sequence absent"},
		{"unowned", "CREATE SEQUENCE ids; ", "nextval('ids')", "unknown or foreign migration sequence ids"},
		{"ambiguous", owned + "ALTER TABLE account ADD COLUMN epoch bigint DEFAULT nextval('ids'); ", "nextval('ids')", "ambiguous migration sequence owner ids"},
		{"setval", owned, "setval('ids', 1)", "unsupported migration function setval"},
		{"nonliteral", owned, "nextval(NEW.id)", "requires an unqualified literal name"},
		{"computed literal", owned, "nextval('id' || 's')", "requires an unqualified literal name"},
		{"qualified literal", owned, "nextval('public.ids')", "requires an unqualified literal name"},
		{"regclass cast", owned, "nextval('ids'::regclass)", "requires an unqualified literal name"},
		{"extra arguments", owned, "nextval('ids', 1)", "requires one literal name"},
		{"qualified function", owned, "pg_catalog.nextval('ids')", "unsupported migration function pg_catalog.nextval"},
		{"duplicate sequence", owned + "CREATE SEQUENCE ids; ", "nextval('ids')", "duplicate migration sequence ids"},
		{"qualified definition", "CREATE SEQUENCE public.ids; ", "nextval('ids')", "qualified migration sequence ids"},
		{"unknown default sequence", "ALTER TABLE message ADD COLUMN epoch bigint DEFAULT nextval('absent'); ", "1", "unknown migration sequence owner absent"},
		{"nonliteral default", "CREATE SEQUENCE ids; ALTER TABLE message ADD COLUMN epoch bigint DEFAULT nextval('id' || 's'); ", "1", "requires an unqualified literal name"},
		{"nested default cannot assign ownership", "CREATE SEQUENCE ids; ALTER TABLE message ADD COLUMN epoch bigint DEFAULT (nextval('ids') + 1); ", "1", "unknown or foreign migration sequence ids"},
		{"foreign DDL sequence", owned + "ALTER TABLE account ADD CONSTRAINT c CHECK(currval('ids') > 0); ", "1", "foreign migration sequence ids"},
		{"DDL setval", owned + "ALTER TABLE message ADD CONSTRAINT c CHECK(setval('ids', 1) > 0); ", "1", "DDL function setval"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modules := map[string]string{"message": owners["message"], "account": owners["account"]}
			if tt.name == "create default ownership" {
				modules["extra"] = "conversation"
			}
			sql := prefix + tt.setup + "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM " + tt.call + "; RETURN NULL; END $$;" + bind
			err := checkMigrations([]string{"-- +goose Up\n" + sql}, modules)
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
		{name: "remove 00006", remove: "00006.backfill.statement2", want: "00006.backfill.statement2: foreign table organization (write=false, owner=org)"},
		{name: "remove 00007", remove: "00007.function.organization_event_seq_logged", want: "00007.function.organization_event_seq_logged: foreign table event_log (write=false, owner=realtime)"},
		{name: "00006 extra access", target: "00006", old: "SELECT o.id, 'general', true", new: "SELECT o.id, (SELECT email FROM account LIMIT 1), true", want: "00006.backfill.statement2: foreign table account (write=false, owner=identity)"},
		{name: "00007 extra access", target: "00007", old: "IF NEW.event_seq >", new: "IF EXISTS (SELECT FROM account) AND NEW.event_seq >", want: "00007.function.organization_event_seq_logged: foreign table account (write=false, owner=identity)"},
		{name: "00010 guard foreign read", target: "00010", old: "IF NEW.id IS DISTINCT FROM OLD.id THEN", new: "IF NEW.id IS DISTINCT FROM OLD.id AND EXISTS(SELECT FROM account) THEN", want: "00010.function.organization_access_guard: foreign table account (write=false, owner=identity)"},
		{name: "00010 truncate foreign write", target: "00010", old: "UPDATE organization SET access_epoch = NULL;", new: "UPDATE account SET email = 'x';", want: "00010.function.member_access_truncated: foreign table account (write=true, owner=identity)"},
		{name: "00007 dynamic", target: "00007", old: "BEGIN", new: "BEGIN EXECUTE 'SELECT 1';", want: "00007.function.organization_event_seq_logged: unsupported migration routine node PLpgSQL_stmt_dynexecute"},
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
		{"foreign key cannot be exempted", "ALTER TABLE message ADD COLUMN account_id int REFERENCES account(id) ON UPDATE CASCADE", "00001.ddl.statement3 message write Reviewed", "cross-module foreign key to account"},
		{"backfill cannot exempt routine", "CREATE FUNCTION statement5() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM id FROM account; RETURN NULL; END $$; CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION statement5(); UPDATE message SET id=(SELECT id FROM account)", "00001.backfill.statement5 account read Maintainer ruling #677: backfill only", "00001.function.statement5: foreign table account"},
		{"routine cannot exempt backfill", "CREATE FUNCTION statement5() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION statement5(); UPDATE message SET id=(SELECT id FROM account)", "00001.function.statement5 account read Reviewed", "00001.backfill.statement5: foreign table account"},
		{"reviewed write", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO account VALUES(1); RETURN NULL; END $$;" + bind, "00001.function.f account write Maintainer ruling #677", ""},
		{"read does not exempt write", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO account VALUES(1); RETURN NULL; END $$;" + bind, "00001.function.f account read Reviewed", "write=true"},
		{"write does not exempt read", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM account; RETURN NULL; END $$;" + bind, "00001.function.f account write Reviewed", "write=false"},
		{"object scope", "UPDATE message SET id=(SELECT id FROM account)", "00001.other account read Reviewed", "foreign table account"},
		{"stale", "UPDATE message SET id=1", "00001.backfill.statement3 account read Reviewed", "stale migration exemption"},
		{"same module stale", "UPDATE message SET id=1", "00001.backfill.statement3 message write Reviewed", "stale migration exemption"},
		{"unknown owner cannot be exempted", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM 1 FROM account; RETURN NULL; END $$;", "00001.function.f account read Reviewed", "unbound migration function"},
		{"call cannot be exempted", "CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;" + bind + " UPDATE message SET id=f()", "00001.backfill.statement5 account read Reviewed", "call to migration-defined function"},
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
