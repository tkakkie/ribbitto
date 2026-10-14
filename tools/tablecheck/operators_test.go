package tablecheck

import (
	"fmt"
	"strings"
	"testing"
)

func TestOperatorLocations(t *testing.T) {
	owners := map[string]string{"message": "conversation"}
	for _, form := range []struct{ name, sql, allowed string }{
		{"any", "SELECT id FROM message WHERE id %s ANY (SELECT id FROM message)", "="},
		{"all", "SELECT id FROM message WHERE id %s ALL (SELECT id FROM message)", "="},
		{"sort", "SELECT id FROM message ORDER BY id USING %s", "<"},
	} {
		for _, op := range []string{"===", "OPERATOR(public.<)"} {
			t.Run(form.name+"/"+op, func(t *testing.T) {
				err := checkSQL(fmt.Sprintf(form.sql, op), "conversation", "conversation.Q", owners, nil)
				if err == nil || !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "operator") {
					t.Fatalf("want operator refusal, got %v", err)
				}
				if err := checkSQL(fmt.Sprintf(form.sql, form.allowed), "conversation", "conversation.Q", owners, nil); err != nil {
					t.Fatalf("allowed operator: %v", err)
				}
			})
		}
	}
}

func TestSetOperationInto(t *testing.T) {
	for _, sql := range setOperationIntoQueries() {
		t.Run(sql, func(t *testing.T) {
			err := checkSQL(sql, "conversation", "conversation.Q", map[string]string{"message": "conversation", "account": "identity"}, nil)
			if err == nil || !strings.Contains(err.Error(), "unsupported SELECT INTO") {
				t.Fatalf("want SELECT INTO refusal, got %v", err)
			}
		})
	}
}

func setOperationIntoQueries() []string {
	return []string{
		"SELECT id INTO account FROM message UNION SELECT id FROM message",
		"SELECT id FROM message UNION SELECT id INTO account FROM message",
		"SELECT id FROM message UNION (SELECT id FROM message UNION SELECT id INTO account FROM message)",
	}
}

func TestMigrationOperatorLocations(t *testing.T) {
	for _, language := range []string{"sql", "plpgsql"} {
		for _, form := range []struct{ name, sql, allowed string }{
			{"any", "SELECT id FROM message WHERE id %s ANY (SELECT id FROM message)", ">="},
			{"all", "SELECT id FROM message WHERE id %s ALL (SELECT id FROM message)", "<>"},
			{"sort", "SELECT id FROM message ORDER BY id USING %s", "<"},
		} {
			for _, op := range []string{"===", "OPERATOR(public.<)"} {
				t.Run(language+"/"+form.name+"/"+op, func(t *testing.T) {
					err := checkRoutineQuery(language, fmt.Sprintf(form.sql, op))
					if err == nil || !strings.Contains(err.Error(), "unsupported migration operator") {
						t.Fatalf("want migration operator refusal, got %v", err)
					}
					if err := checkRoutineQuery(language, fmt.Sprintf(form.sql, form.allowed)); err != nil {
						t.Fatalf("allowed migration operator: %v", err)
					}
				})
			}
		}
	}
}

func TestMigrationSetOperationInto(t *testing.T) {
	for _, language := range []string{"sql", "plpgsql"} {
		for _, sql := range setOperationIntoQueries() {
			t.Run(language+"/"+sql, func(t *testing.T) {
				err := checkRoutineQuery(language, sql)
				if err == nil || !strings.Contains(err.Error(), "unsupported SELECT INTO") {
					t.Fatalf("want SELECT INTO refusal, got %v", err)
				}
			})
		}
	}
}

func checkRoutineQuery(language, query string) error {
	if language == "plpgsql" {
		// PERFORM retains SQL INTO clauses; PL/pgSQL SELECT INTO a variable
		// is an assignment and is already covered by the migration fixtures.
		query = "BEGIN PERFORM " + strings.TrimPrefix(query, "SELECT ") + "; RETURN NEW; END"
	}
	sql := "-- +goose Up\nCREATE TABLE message(id int); CREATE TABLE account(id int); " +
		"CREATE FUNCTION f() RETURNS trigger LANGUAGE " + language + " AS $$ " + query + " $$; " +
		"CREATE TRIGGER t AFTER INSERT ON message FOR EACH ROW EXECUTE FUNCTION f();"
	return checkMigrations([]string{sql}, map[string]string{"message": "conversation", "account": "identity"})
}
