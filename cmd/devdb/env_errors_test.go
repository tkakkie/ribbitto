package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnvironmentRefusesUnusableSetups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dir   func(t *testing.T) string
		check func(t *testing.T, err error)
	}{
		{"outside any repository", func(t *testing.T) string {
			dir := resolve(t.TempDir())
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
			return dir
		}, func(t *testing.T, err error) {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("outside any repository: error = %v (%T), want *exec.ExitError", err, err)
			}
		}},
		{"malformed .env.local", func(t *testing.T) string {
			f := newFixture(t)
			if err := os.WriteFile(filepath.Join(f.linked, ".env.local"), []byte("AI_DATABASE\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return f.linked
		}, func(t *testing.T, err error) {
			if err == nil || err.Error() != "malformed .env.local" {
				t.Fatalf("malformed .env.local: error = %v, want %q", err, "malformed .env.local")
			}
		}},
		{".env.local is a directory", func(t *testing.T) string {
			f := newFixture(t)
			local := filepath.Join(f.linked, ".env.local")
			if err := os.Remove(local); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(local, 0o755); err != nil {
				t.Fatal(err)
			}
			return f.linked
		}, func(t *testing.T, err error) {
			if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf(".env.local is a directory: error = %v, want non-nil error other than %v", err, os.ErrNotExist)
			}
		}},
		{"missing ai-env.json", func(t *testing.T) string {
			t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
			f := newFixture(t)
			if err := os.Remove(filepath.Join(f.common, "ai-env.json")); err != nil {
				t.Fatal(err)
			}
			return f.linked
		}, func(t *testing.T, err error) {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing ai-env.json: error = %v, want %v", err, os.ErrNotExist)
			}
		}},
		{"non-JSON ai-env.json", func(t *testing.T) string {
			t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
			f := newFixture(t)
			if err := os.WriteFile(filepath.Join(f.common, "ai-env.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return f.linked
		}, func(t *testing.T, err error) {
			var syntaxErr *json.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("non-JSON ai-env.json: error = %v (%T), want *json.SyntaxError", err, err)
			}
		}},
		{"unset admin URL", func(t *testing.T) string {
			t.Setenv("RIBBITTO_TEST_DATABASE_URL", admin)
			f := newFixture(t)
			t.Setenv("RIBBITTO_TEST_DATABASE_URL", "")
			return f.linked
		}, func(t *testing.T, err error) {
			if err == nil || err.Error() != "admin configuration unset" {
				t.Fatalf("unset admin URL: error = %v, want %q", err, "admin configuration unset")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t)
			env, err := environment(dir)
			if env != nil {
				t.Errorf("%s: environment(%q) = %v, want nil", tc.name, dir, env)
			}
			tc.check(t, err)
		})
	}
}

func TestCurrentID(t *testing.T) {
	base := resolve(t.TempDir())
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	live := map[string]string{
		"main":            filepath.Join(base, "main"),
		"worktree:linked": filepath.Join(base, "linked"),
	}
	for _, tc := range []struct {
		name, root, want string
	}{
		{"main", filepath.Join(base, "main"), "main"},
		{"linked", filepath.Join(base, "linked"), "worktree:linked"},
		{"symlinked linked", filepath.Join(alias, "linked"), "worktree:linked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := currentID(tc.root, live)
			if err != nil {
				t.Errorf("%s: currentID(%q) error = %v, want nil", tc.name, tc.root, err)
			}
			if got != tc.want {
				t.Errorf("%s: currentID(%q) = %q, want %q", tc.name, tc.root, got, tc.want)
			}
		})
	}
	t.Run("unlisted", func(t *testing.T) {
		root := filepath.Join(base, "unlisted")
		got, err := currentID(root, live)
		if got != "" {
			t.Errorf("unlisted: currentID(%q) = %q, want %q", root, got, "")
		}
		const wantErr = "current Git worktree not listed; refusing cleanup"
		var f failure
		if !errors.As(err, &f) || string(f) != wantErr {
			t.Fatalf("unlisted: currentID(%q) error = %v (%T), want failure %q", root, err, err, wantErr)
		}
	})
}

func TestWithLock(t *testing.T) {
	t.Run("returns callback error and creates lock file", func(t *testing.T) {
		common := t.TempDir()
		sentinel := errors.New("callback failed")
		runs := 0
		err := withLock(common, syscall.LOCK_EX, func() error {
			runs++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("returns callback error and creates lock file: withLock error = %v, want %v", err, sentinel)
		}
		if runs != 1 {
			t.Errorf("returns callback error and creates lock file: callback runs = %d, want 1", runs)
		}
		if _, err := os.Stat(filepath.Join(common, "ai-env.lock")); err != nil {
			t.Errorf("returns callback error and creates lock file: stat ai-env.lock = %v, want nil", err)
		}
	})
	t.Run("holds lock during callback and releases after", func(t *testing.T) {
		common := t.TempDir()
		innerRuns := 0
		var innerErr error
		err := withLock(common, syscall.LOCK_SH, func() error {
			innerErr = withLock(common, syscall.LOCK_EX|syscall.LOCK_NB, func() error {
				innerRuns++
				return nil
			})
			return nil
		})
		if err != nil {
			t.Fatalf("holds lock during callback and releases after: outer withLock error = %v, want nil", err)
		}
		if !errors.Is(innerErr, syscall.EWOULDBLOCK) {
			t.Errorf("holds lock during callback and releases after: nested withLock error = %v, want %v", innerErr, syscall.EWOULDBLOCK)
		}
		if innerRuns != 0 {
			t.Errorf("holds lock during callback and releases after: nested callback runs = %d, want 0", innerRuns)
		}
		afterRuns := 0
		if err := withLock(common, syscall.LOCK_EX|syscall.LOCK_NB, func() error {
			afterRuns++
			return nil
		}); err != nil {
			t.Errorf("holds lock during callback and releases after: post-release withLock error = %v, want nil", err)
		}
		if afterRuns != 1 {
			t.Errorf("holds lock during callback and releases after: post-release callback runs = %d, want 1", afterRuns)
		}
	})
	t.Run("missing common directory", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "missing")
		runs := 0
		err := withLock(common, syscall.LOCK_EX, func() error {
			runs++
			return nil
		})
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("missing common directory: withLock error = %v, want %v", err, os.ErrNotExist)
		}
		if runs != 0 {
			t.Errorf("missing common directory: callback runs = %d, want 0", runs)
		}
	})
}
