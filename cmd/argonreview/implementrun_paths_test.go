package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func implementRunSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			files[path] = "dir"
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[path] = "link:" + target
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestImplementRunValidationWritesNothing(t *testing.T) {
	for _, name := range []string{"trust", "unknown", "positional", "repeat", "relative", "unclean", "HOME", "hash-empty", "hash-short", "hash-nonhex", "hooks-mismatch", "settings-mismatch", "agent-source-link", "home-link-component", "log-link", "log-link-component", "git-link", "git-hardlink", "prompt-in-tree", "prompt-link", "prompt-cap", "log-in-tree", "log-containing-tree", "log-in-home", "git-in-tree", "git-in-home", "nonempty-logs", "nested-home", "target-link", "target-directory", "brain-link", "bad-timeout", "empty-model", "tree-trailing-space", "tree-trailing-separator", "tree-invalid-utf8"} {
		t.Run(name, func(t *testing.T) {
			x := newImplementRun(t)
			switch name {
			case "trust":
				x.deps.trust = func() error { return errors.New("untrusted") }
				x.args = []string{"--unknown"}
			case "unknown":
				x.args = append(x.args, "--unknown")
			case "positional":
				x.args = append(x.args, "extra")
			case "repeat":
				x.args = append(x.args, "--home", x.f.home)
			case "relative":
				x.set("worktree", "relative")
			case "unclean":
				x.set("worktree", x.f.tree+"/../tree")
			case "tree-trailing-space", "tree-trailing-separator":
				suffix := " "
				if name == "tree-trailing-separator" {
					suffix = "\x1c"
				}
				path := x.f.tree + suffix
				implementCheckMkdir(t, path)
				x.set("worktree", path)
			case "tree-invalid-utf8":
				x.set("worktree", x.f.tree+string([]byte{255}))
			case "HOME":
				x.deps.accountHome = "relative"
			case "hash-empty":
				x.set("shared-settings-sha256", "")
			case "hash-short":
				x.set("shared-settings-sha256", "abcd")
			case "hash-nonhex":
				x.set("shared-settings-sha256", strings.Repeat("g", 64))
			case "hooks-mismatch", "settings-mismatch":
				write(t, filepath.Join(x.f.home, "implementer", strings.TrimSuffix(name, "-mismatch")+".json"), "changed")
			case "agent-source-link":
				path := filepath.Join(x.f.home, "implementer/argon-implementer.md")
				implementCheckRemove(t, path)
				symlink(t, x.f.saved, path)
			case "home-link-component":
				link := filepath.Join(x.f.root, "alias")
				symlink(t, x.f.root, link)
				x.set("home", filepath.Join(link, "home"))
			case "log-link":
				symlink(t, x.fake, x.logs)
			case "log-link-component":
				link := filepath.Join(x.f.root, "alias")
				symlink(t, x.f.root, link)
				x.set("log-dir", filepath.Join(link, "logs"))
			case "git-link", "prompt-link":
				path := filepath.Join(x.f.root, "alias")
				symlink(t, x.f.saved, path)
				flag := "git-file"
				if name == "prompt-link" {
					flag = "prompt-file"
				}
				x.set(flag, path)
			case "git-hardlink":
				if err := os.Link(x.f.saved, filepath.Join(x.f.tree, "alias")); err != nil {
					t.Fatal(err)
				}
			case "prompt-in-tree":
				path := filepath.Join(x.f.tree, "prompt")
				write(t, path, "prompt")
				x.set("prompt-file", path)
			case "prompt-cap":
				write(t, filepath.Join(x.f.root, "prompt"), strings.Repeat("x", (1<<20)+1))
			case "log-in-tree":
				x.set("log-dir", filepath.Join(x.f.tree, "logs"))
			case "log-containing-tree":
				x.set("log-dir", x.f.root)
			case "log-in-home":
				x.set("log-dir", filepath.Join(x.f.home, "logs"))
			case "git-in-tree", "git-in-home":
				parent := x.f.tree
				if name == "git-in-home" {
					parent = x.f.home
				}
				path := filepath.Join(parent, "saved")
				write(t, path, "saved")
				x.set("git-file", path)
			case "nonempty-logs":
				write(t, filepath.Join(x.logs, "existing"), "do not replace")
			case "nested-home":
				x.set("home", x.f.root)
			case "target-link", "target-directory":
				path := filepath.Join(x.f.home, ".gemini/config/hooks.json")
				implementCheckRemove(t, path)
				if name == "target-link" {
					symlink(t, x.f.saved, path)
				} else {
					implementCheckMkdir(t, path)
				}
			case "brain-link":
				symlink(t, x.fake, filepath.Join(x.f.home, ".gemini/antigravity-cli/brain"))
			case "bad-timeout":
				x.set("timeout", "0")
			case "empty-model":
				x.set("model", "")
			}
			before := implementRunSnapshot(t, x.f.root)
			var out, stderr bytes.Buffer
			code := implementRunCommand(x.args, &out, &stderr, x.deps)
			if name == "trust" && (code != 1 || !strings.Contains(stderr.String(), "untrusted") || strings.Contains(stderr.String(), "flag provided")) {
				t.Fatalf("trust did not precede parsing: exit=%d stderr=%s", code, stderr.String())
			}
			if code == 0 || out.Len() != 0 || stderr.Len() == 0 || x.checks != 0 || len(x.order) > 1 {
				t.Fatalf("not refused before effects: exit=%d checks=%d order=%v out=%s stderr=%s", code, x.checks, x.order, out.String(), stderr.String())
			}
			if after := implementRunSnapshot(t, x.f.root); !reflect.DeepEqual(before, after) {
				t.Fatal("validation failure changed files")
			}
		})
	}
}

func TestImplementRunTemporaryCreationFailure(t *testing.T) {
	x := newImplementRun(t)
	existing := x.f.tree + ".tmp-existing"
	implementCheckMkdir(t, existing)
	write(t, filepath.Join(existing, "keep"), "unchanged")
	x.deps.mkdirTemp = func(parent, pattern string) (string, error) {
		if parent != filepath.Dir(x.f.tree) || pattern != filepath.Base(x.f.tree)+".tmp-*" {
			t.Fatalf("unsafe temporary creation %q %q", parent, pattern)
		}
		return "", os.Mkdir(existing, 0o700)
	}
	// Lock creation now precedes TMPDIR creation. Seed that persistent file
	// so the snapshot still proves failed creation never places configuration.
	lock, err := implementRunLock(x.f.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	before := implementRunSnapshot(t, x.f.home)
	status := x.run(t, 1)
	if !strings.Contains(status, "creating TMPDIR") || x.checks != 0 || !reflect.DeepEqual(before, implementRunSnapshot(t, x.f.home)) {
		t.Fatalf("temporary failure wrote configuration: %s", status)
	}
	data, _ := os.ReadFile(filepath.Join(existing, "keep"))
	if string(data) != "unchanged" {
		t.Fatal("reused existing TMPDIR")
	}
}

func TestImplementRunConfinedWritesAndRemoval(t *testing.T) {
	x := newImplementRun(t)
	// Same-named output in writable directories, and a swapped TMPDIR child,
	// cannot replace the held log descriptors or the saved comparison bytes.
	write(t, filepath.Join(x.fake, "mutate"), `echo forged > status
echo forged > out.jsonl
echo forged > "$TMPDIR/status"
echo forged > "$TMPDIR/out.jsonl"
ln -s '`+x.f.saved+`' "$TMPDIR/comparison"
ln -s '`+x.logs+`' "$TMPDIR/logs"
mkdir -p "$HOME/.gemini/antigravity-cli/brain/nested"
echo conversation > "$HOME/.gemini/antigravity-cli/brain/nested/file"
ln -s '`+x.fake+`' "$HOME/.gemini/antigravity-cli/brain/link"
`)
	x.run(t, 0)
	data, _ := os.ReadFile(filepath.Join(x.logs, "out.jsonl"))
	if string(data) != okResult {
		t.Fatalf("transcript changed: %q", data)
	}
	data, _ = os.ReadFile(filepath.Join(x.logs, "status"))
	if !strings.Contains(string(data), "counts=true") || strings.Contains(string(data), "forged") {
		t.Fatalf("status changed: %q", data)
	}
	data, _ = os.ReadFile(x.f.saved)
	if string(data) != "gitdir: fixture\n" {
		t.Fatalf("comparison changed: %q", data)
	}
	data, err := os.ReadFile(filepath.Join(x.fake, "stdout"))
	if err != nil || string(data) != okResult {
		t.Fatalf("removal followed brain symlink: %q %v", data, err)
	}
	x.cleaned(t)
}

func TestImplementRunWriteRefusesSwappedSymlinks(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaf", true: "parent"}[parent], func(t *testing.T) {
			x := newImplementRun(t)
			step := x.deps.step
			x.deps.step = func(name string) error {
				if name == "place-hooks" {
					path := filepath.Join(x.f.home, ".gemini/config/hooks.json")
					target := x.f.saved
					if parent {
						path = filepath.Dir(path)
						target = x.fake
					}
					implementCheckRemove(t, path)
					symlink(t, target, path)
				}
				return step(name)
			}
			x.deps.check = func(_ []string, _, _ io.Writer) int { return 1 }
			x.run(t, 1)
			data, _ := os.ReadFile(x.f.saved)
			if string(data) != "gitdir: fixture\n" {
				t.Fatal("write followed swapped symlink")
			}
			if _, err := os.Stat(filepath.Join(x.fake, "hooks.json")); !os.IsNotExist(err) {
				t.Fatal("write followed swapped parent")
			}
		})
	}
}

func TestImplementRunTemporarySymlinkSwap(t *testing.T) {
	x := newImplementRun(t)
	write(t, filepath.Join(x.fake, "mutate"), `mv "$TMPDIR" "$TMPDIR.saved"
ln -s '`+x.fake+`' "$TMPDIR"
`)
	status := x.run(t, 1)
	if !strings.Contains(status, "cleanup-remove-tmp=false") || !strings.Contains(status, "post-check=0") || !strings.Contains(status, "recovery-inspect=") {
		t.Fatalf("missing cleanup failure/recovery: %s", status)
	}
	data, err := os.ReadFile(filepath.Join(x.fake, "stdout"))
	if err != nil || string(data) != okResult {
		t.Fatal("TMPDIR cleanup followed a swapped symlink")
	}
	if _, err := os.Stat(filepath.Join(x.f.home, ".gemini/config/agents")); !os.IsNotExist(err) {
		t.Fatal("cleanup failure skipped later agent removal")
	}
}
