package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, root, ".gitignore", "ignored.txt\n")
	write(t, root, "tracked.txt", "tracked\n")
	run("add", ".gitignore", "tracked.txt")
	run("-c", "user.name=Fingerprint test", "-c", "user.email=fingerprint-test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Baseline")
	return root
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprint(t *testing.T) {
	root := repository(t)
	// The steps run in order on one repository, as in the bash test: each
	// must change the fingerprint (or, for same, leave it), and each
	// fingerprint must succeed.
	for _, step := range []struct {
		name   string
		change func()
		same   bool
	}{
		{"unchanged tree", func() {}, true},
		{"tracked edit", func() { write(t, root, "tracked.txt", "tracked\nmore") }, false},
		{"new untracked file", func() { write(t, root, "plain.txt", "a") }, false},
		{"rewritten untracked file", func() { write(t, root, "plain.txt", "b") }, false},
		{"new file in an untracked directory", func() { write(t, root, "gen/one.go", "a") }, false},
		{"second file in that directory", func() { write(t, root, "gen/two.go", "a") }, false},
		{"renamed untracked file", func() {
			if err := os.Rename(filepath.Join(root, "gen/two.go"), filepath.Join(root, "gen/three.go")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"ignored file", func() { write(t, root, "ignored.txt", "a") }, true},
		{"new quoted name", func() { write(t, root, "café.txt", "a") }, false},
		{"rewritten quoted name", func() { write(t, root, "café.txt", "b") }, false},
		{"name with a quote, backslash and tab", func() { write(t, root, "q\"b\\c\td.txt", "a") }, false},
		{"new name with a space", func() { write(t, root, "with space.txt", "a") }, false},
		{"rewritten name with a space", func() { write(t, root, "with space.txt", "b") }, false},
		{"new name with a newline", func() { write(t, root, "new\nline.txt", "a") }, false},
		{"rewritten name with a newline", func() { write(t, root, "new\nline.txt", "b") }, false},
		{"file named -", func() { write(t, root, "-", "a") }, false},
		{"file after -", func() { write(t, root, "plain.txt", "c") }, false},
		{"file after a quoted name", func() { write(t, root, "zz_after.txt", "d") }, false},
		{"rewritten file after a quoted name", func() { write(t, root, "zz_after.txt", "e") }, false},
		// Bytes moving between adjacent files keep the concatenation the
		// same, so only the per-file size can tell the two states apart.
		{"two adjacent files", func() { write(t, root, "move1.txt", "ab"); write(t, root, "move2.txt", "") }, false},
		{"a byte moved to the next file", func() { write(t, root, "move1.txt", "a"); write(t, root, "move2.txt", "b") }, false},
	} {
		before, err := fingerprint(root)
		if err != nil {
			t.Fatalf("%s: fingerprint before: %v", step.name, err)
		}
		step.change()
		after, err := fingerprint(root)
		if err != nil {
			t.Fatalf("%s: fingerprint after: %v", step.name, err)
		}
		if (after == before) != step.same {
			t.Errorf("%s: fingerprint unchanged = %v, want %v", step.name, after == before, step.same)
		}
	}
}

func TestFingerprintFailsOnAnUnreadableFile(t *testing.T) {
	root := repository(t)
	write(t, root, "unreadable.txt", "secret")
	path := filepath.Join(root, "unreadable.txt")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("running with permission to read it")
	}
	if _, err := fingerprint(root); err == nil {
		t.Fatal("an unreadable untracked file did not fail the fingerprint")
	}
}

func TestCheck(t *testing.T) {
	root := repository(t)
	fp := func() (string, error) { return fingerprint(root) }
	if err := check(fp, func() error { return nil }); err != nil {
		t.Fatalf("a command that changes nothing: %v", err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"created file", func() error { write(t, root, "generated.go", "a"); return nil }},
		{"rewritten untracked file", func() error { write(t, root, "generated.go", "b"); return nil }},
	} {
		if err := check(fp, tc.run); !errors.As(err, new(errChanged)) {
			t.Errorf("%s: err = %v, want errChanged", tc.name, err)
		}
	}
	if err := check(fp, func() error { return errors.New("exit status 1") }); err == nil || errors.As(err, new(errChanged)) {
		t.Errorf("a failing command: err = %v, want its failure", err)
	}
}

// A fingerprint that fails on one call but returns the same value each time
// must fail the check, whichever call fails.
func TestCheckFailsWhenEitherFingerprintFails(t *testing.T) {
	for _, failing := range []int{1, 2} {
		calls := 0
		fp := func() (string, error) {
			calls++
			if calls == failing {
				return "same", errors.New("fingerprint failed")
			}
			return "same", nil
		}
		if err := check(fp, func() error { return nil }); err == nil {
			t.Errorf("check passed although fingerprint call %d failed", failing)
		}
	}
	if err := check(func() (string, error) { return "same", nil }, func() error { return nil }); err != nil {
		t.Errorf("both fingerprints succeeding: %v", err)
	}
}

func TestFingerprintStagedEdit(t *testing.T) {
	root := repository(t)
	before, err := fingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "tracked.txt", "staged edit\n")
	cmd := exec.Command("git", "add", "tracked.txt")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	after, err := fingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("an edited and staged tracked file left the fingerprint unchanged")
	}
}

func TestFingerprintSymlinks(t *testing.T) {
	root := repository(t)
	outside := t.TempDir()
	write(t, outside, "target.txt", "a")
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprint(root)
	if err != nil {
		t.Fatalf("a symlink to a directory failed the fingerprint: %v", err)
	}
	// Git stores the link, not what it points to.
	write(t, outside, "target.txt", "b")
	if after, err := fingerprint(root); err != nil || after != before {
		t.Fatalf("changing a link's target outside the worktree: changed = %v, err = %v", after != before, err)
	}
	if err := os.Remove(filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "other.txt"), filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if after, err := fingerprint(root); err != nil || after == before {
		t.Fatalf("retargeting a link: changed = %v, err = %v", after != before, err)
	}
}
