package main

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a main checkout with one linked worktree and a reservation for
// the linked one, written in the format make ai-env writes today.
type fixture struct {
	main, linked, common, name string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := resolve(t.TempDir())
	f := &fixture{main: filepath.Join(base, "main"), linked: filepath.Join(base, "linked")}
	if err := os.Mkdir(f.main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.main, "init", "-q")
	gitIn(t, f.main, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, f.main, "worktree", "add", "-q", "--detach", f.linked)
	f.common = filepath.Join(f.main, ".git")
	f.name = filepath.Base(f.linked)
	f.reserve(t, `{"worktree:`+f.name+`": {"path": "`+f.linked+`", "database_path": "`+f.linked+`", "ports": [20001, 20002], "metadata_token": "token"}}`, "token")
	f.local(t, map[string]string{"AI_DATABASE": databaseName(f.linked), "AI_APP_PORT": "20001", "AI_METRICS_PORT": "20002"})
	return f
}

func (f *fixture) reserve(t *testing.T, registry, token string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.common, "ai-env.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.common, "worktrees", f.name, "ribbitto-ai-env"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) local(t *testing.T, values map[string]string) {
	t.Helper()
	var b strings.Builder
	for _, key := range []string{"AI_DATABASE", "AI_APP_PORT", "AI_METRICS_PORT"} {
		b.WriteString(key + "=" + values[key] + "\n")
	}
	if err := os.WriteFile(filepath.Join(f.linked, ".env.local"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lookup returns the only value for key, failing on a duplicate: the first
// one would win after exec.
func lookup(t *testing.T, env []string, key string) string {
	t.Helper()
	var values []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			values = append(values, v)
		}
	}
	if len(values) > 1 {
		t.Fatalf("%s appears %d times", key, len(values))
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

const admin = "postgres://user:pass@127.0.0.1:5432/postgres?sslmode=disable"

func TestEnvironment(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	f := newFixture(t)
	env, err := environment(f.linked)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(lookup(t, env, "RIBBITTO_DATABASE_URL"))
	if err != nil || u.Path != "/"+databaseName(f.linked) || u.Host != "127.0.0.1:5432" || u.User.String() != "user:pass" || u.RawQuery != "sslmode=disable" {
		t.Fatalf("database URL fields: path %q host %q query %q, err %v", u.Path, u.Host, u.RawQuery, err)
	}
	if lookup(t, env, "RIBBITTO_ADDR") != "127.0.0.1:20001" || lookup(t, env, "RIBBITTO_DEV_METRICS_ADDR") != "127.0.0.1:20002" {
		t.Fatalf("addresses: %q %q", lookup(t, env, "RIBBITTO_ADDR"), lookup(t, env, "RIBBITTO_DEV_METRICS_ADDR"))
	}
	// A subdirectory uses the worktree root's reservation.
	child := filepath.Join(f.linked, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if env, err := environment(child); err != nil || lookup(t, env, "RIBBITTO_ADDR") != "127.0.0.1:20001" {
		t.Fatalf("subdirectory: %v", err)
	}
}

func TestEnvironmentWithoutLocalConfiguration(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.linked, ".env.local")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIBBITTO_DATABASE_URL", "kept")
	env, err := environment(f.linked)
	if err != nil || !slices.Equal(env, os.Environ()) {
		t.Fatalf("environment without .env.local changed the environment: %v", err)
	}
}

// Every check runs before the admin URL is read, so a bad local value is
// reported even when the admin configuration is unusable.
func TestEnvironmentRefusesUnownedValues(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", "invalid-admin-config")
	for _, tc := range []struct {
		field, value string
	}{
		{"AI_DATABASE", "ribbitto"},
		{"AI_DATABASE", "x?host=evil.example"},
		{"AI_APP_PORT", "1"},
		{"AI_METRICS_PORT", "2"},
		{"AI_APP_PORT", "not-an-integer"},
		{"AI_METRICS_PORT", "70000"},
	} {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			f := newFixture(t)
			values := map[string]string{"AI_DATABASE": databaseName(f.linked), "AI_APP_PORT": "20001", "AI_METRICS_PORT": "20002"}
			values[tc.field] = tc.value
			f.local(t, values)
			_, err := environment(f.linked)
			if err == nil || !strings.HasPrefix(err.Error(), ".env.local "+tc.field+" does not match") {
				t.Fatalf("environment = %v", err)
			}
		})
	}
}

func TestEnvironmentRefusesBadReservations(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	for _, tc := range []struct {
		name, registry, token, want string
	}{
		{"different token", `"metadata_token": "token"`, "other", "worktree reservation is stale"},
		{"empty token", `"metadata_token": ""`, "", "worktree reservation is stale"},
		{"no token", `"metadata_token": null`, "token", "worktree reservation is stale"},
		{"one port", `"ports": [20001], "metadata_token": "token"`, "token", "worktree port reservations invalid"},
		{"fractional port", `"ports": [20001.0, 20002], "metadata_token": "token"`, "token", ".env.local AI_APP_PORT does not match"},
		{"boolean port", `"ports": [true, 20002], "metadata_token": "token"`, "token", ".env.local AI_APP_PORT does not match"},
		{"port out of range", `"ports": [20001, 70000], "metadata_token": "token"`, "token", ".env.local AI_METRICS_PORT does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ports := `"ports": [20001, 20002], `
			if strings.Contains(tc.registry, `"ports"`) {
				ports = ""
			}
			f.reserve(t, `{"worktree:`+f.name+`": {"path": "`+f.linked+`", "database_path": "`+f.linked+`", `+ports+tc.registry+`}}`, tc.token)
			if _, err := environment(f.linked); err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("environment = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("no entry", func(t *testing.T) {
		f := newFixture(t)
		f.reserve(t, `{}`, "token")
		if _, err := environment(f.linked); err == nil || !strings.HasPrefix(err.Error(), ".env.local AI_DATABASE does not match") {
			t.Fatalf("environment = %v", err)
		}
	})
}

// Git keeps a linked worktree's metadata name through a move, a relative
// backlink and a symlinked spelling, so the reservation still applies.
func TestEnvironmentFollowsTheWorktreeID(t *testing.T) {
	t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
	t.Run("moved", func(t *testing.T) {
		f := newFixture(t)
		moved := filepath.Join(filepath.Dir(f.linked), "moved")
		gitIn(t, f.main, "worktree", "move", f.linked, moved)
		if env, err := environment(moved); err != nil || !strings.Contains(lookup(t, env, "RIBBITTO_DATABASE_URL"), databaseName(f.linked)) {
			t.Fatalf("moved worktree: %v", err)
		}
	})
	t.Run("relative backlink", func(t *testing.T) {
		f := newFixture(t)
		gitIn(t, f.main, "config", "worktree.useRelativePaths", "true")
		gitIn(t, f.main, "worktree", "repair", f.linked)
		backlink, err := os.ReadFile(filepath.Join(f.common, "worktrees", f.name, "gitdir"))
		if err != nil || filepath.IsAbs(strings.TrimSpace(string(backlink))) {
			t.Skipf("this Git keeps absolute backlinks: %q, %v", backlink, err)
		}
		if _, err := environment(f.linked); err != nil {
			t.Fatalf("relative backlink: %v", err)
		}
	})
	t.Run("symlinked spelling", func(t *testing.T) {
		f := newFixture(t)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(filepath.Dir(f.linked), alias); err != nil {
			t.Fatal(err)
		}
		if _, err := environment(filepath.Join(alias, filepath.Base(f.linked))); err != nil {
			t.Fatalf("symlinked spelling: %v", err)
		}
	})
}

func TestDatabaseURL(t *testing.T) {
	got, err := databaseURL("postgresql://u:p%40ss@db.example:6432/postgres?sslmode=require&dbname=postgres&application_name=value%3Fhost%3Dother%26setting%3Dtext&datab%61se=x", "ribbitto_dev_0123")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Scheme != "postgresql" || u.Host != "db.example:6432" || u.User.Username() != "u" || password != "p@ss" || u.Path != "/ribbitto_dev_0123" {
		t.Fatalf("fields changed: scheme %q host %q path %q", u.Scheme, u.Host, u.Path)
	}
	if u.RawQuery != "sslmode=require&application_name=value%3Fhost%3Dother%26setting%3Dtext" {
		t.Fatalf("query = %q", u.RawQuery)
	}
	for _, bad := range []string{"invalid-admin-config", "mysql://u@h/d", "postgres:///postgres"} {
		if _, err := databaseURL(bad, "x"); err == nil || err.Error() != "admin configuration must be a PostgreSQL URL" {
			t.Errorf("databaseURL(%q) = %v", bad, err)
		}
	}
}

// A removed (prunable) worktree keeps its missing tail in order, however
// many levels are gone, and its existing prefix is resolved through links.
func TestResolveKeepsMissingLevelsInOrder(t *testing.T) {
	base := resolve(t.TempDir())
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ in, want string }{
		{filepath.Join(base, "gone"), filepath.Join(base, "gone")},
		{filepath.Join(base, "gone", "nested"), filepath.Join(base, "gone", "nested")},
		{filepath.Join(base, "a", "b", "c"), filepath.Join(base, "a", "b", "c")},
		{filepath.Join(alias, "gone", "nested"), filepath.Join(base, "gone", "nested")},
		{base, base},
	} {
		if got := resolve(tc.in); got != tc.want {
			t.Errorf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
