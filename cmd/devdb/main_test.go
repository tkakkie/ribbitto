package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets a test run this binary as the command itself, so the
// subprocess tests cover main's dispatch, messages and exit status.
func TestMain(m *testing.M) {
	if os.Getenv("DEVDB_TEST_MAIN") == "1" {
		os.Args = append([]string{"ai-db"}, os.Args[1:]...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// command runs this test binary as bin/ai-db with args in dir.
func command(t *testing.T, dir string, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(env, "DEVDB_TEST_MAIN=1")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func without(env []string, keys ...string) []string {
	var out []string
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, "DEVDB_TEST") && !containsKey(keys, key) {
			out = append(out, kv)
		}
	}
	return out
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// Inherited values must not survive next to the worktree's: after exec the
// first occurrence would win.
func TestRunReplacesInheritedValues(t *testing.T) {
	f := newFixture(t)
	env := append(without(os.Environ()),
		"RIBBITTO_TEST_DATABASE_URL="+admin,
		"RIBBITTO_DATABASE_URL=postgres://inherited@elsewhere/inherited",
		"RIBBITTO_ADDR=127.0.0.1:1",
		"RIBBITTO_DEV_METRICS_ADDR=127.0.0.1:2")
	stdout, stderr, code := command(t, f.linked, env, "", "run", "env")
	if code != 0 {
		t.Fatalf("run env: exit %d: %s", code, stderr)
	}
	got := strings.Split(strings.TrimSpace(stdout), "\n")
	if db := lookup(t, got, "RIBBITTO_DATABASE_URL"); !strings.Contains(db, "/"+databaseName(f.linked)) || strings.Contains(db, "inherited") {
		t.Fatalf("RIBBITTO_DATABASE_URL kept the inherited value")
	}
	if lookup(t, got, "RIBBITTO_ADDR") != "127.0.0.1:20001" || lookup(t, got, "RIBBITTO_DEV_METRICS_ADDR") != "127.0.0.1:20002" {
		t.Fatalf("addresses: %q %q", lookup(t, got, "RIBBITTO_ADDR"), lookup(t, got, "RIBBITTO_DEV_METRICS_ADDR"))
	}
}

func TestRunRefusesBeforeRunningTheCommand(t *testing.T) {
	f := newFixture(t)
	f.local(t, map[string]string{"AI_DATABASE": "ribbitto", "AI_APP_PORT": "20001", "AI_METRICS_PORT": "20002"})
	marker := filepath.Join(t.TempDir(), "ran")
	env := append(without(os.Environ()), "RIBBITTO_TEST_DATABASE_URL=invalid-admin-config")
	_, stderr, code := command(t, f.linked, env, "", "run", "touch", marker)
	if code != 1 || !strings.HasPrefix(stderr, ".env.local AI_DATABASE does not match") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}

const canary = "secret-canary"

func TestHealthFailures(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, url, want string
	}{
		{"unset", "", "admin URL not set or invalid\n"},
		{"invalid", "postgres://%zz", "admin URL not set or invalid\n"},
		{"unreachable", "postgres://private:" + canary + "@127.0.0.1:1/postgres?connect_timeout=1", "PostgreSQL stopped or unreachable\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := without(os.Environ(), "RIBBITTO_TEST_DATABASE_URL")
			if tc.url != "" {
				env = append(env, "RIBBITTO_TEST_DATABASE_URL="+tc.url)
			}
			stdout, stderr, code := command(t, dir, env, "", "health")
			if code != 1 || stderr != tc.want || stdout != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, canary) || strings.Contains(stdout+stderr, "postgres://") {
				t.Fatal("output exposes the admin URL")
			}
		})
	}
}

// The success path and a failing statement need the real server.
func TestHealthAndStatementWithDatabase(t *testing.T) {
	url := os.Getenv("RIBBITTO_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("RIBBITTO_REQUIRE_DB") == "1" {
			t.Fatal("RIBBITTO_TEST_DATABASE_URL is required")
		}
		t.Skip("admin configuration unset")
	}
	dir, env := t.TempDir(), without(os.Environ())
	if stdout, stderr, code := command(t, dir, env, "", "health"); code != 0 || stdout != "PostgreSQL reachable\n" || stderr != "" {
		t.Fatalf("health: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, stderr, code := command(t, dir, env, "SELECT deliberately_missing_ai_env_column"); code != 4 || stderr != "PostgreSQL statement failed\n" {
		t.Fatalf("statement: exit %d, stderr %q", code, stderr)
	}
}
