package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Boundary/error cases precede the passing fixtures in each group.
func TestImplementCheckPaths(t *testing.T) {
	for _, name := range []string{"relative", "unclean", "symlink parent", "symlink root", "missing", "file", "home inside", "worktree inside", "equal", "lstat failure", "nesting stat failure", "valid"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			switch name {
			case "relative":
				f.home = "relative"
			case "unclean":
				f.home += "/."
			case "symlink parent":
				link := filepath.Join(f.root, "parent-link")
				implementCheckLink(t, f.root, link)
				f.home = filepath.Join(link, "home")
			case "symlink root":
				link := filepath.Join(f.root, "root-link")
				implementCheckLink(t, f.home, link)
				f.home = link
			case "missing":
				f.home = filepath.Join(f.root, "missing")
			case "file":
				f.home = f.saved
			case "home inside":
				f.home = filepath.Join(f.tree, "home")
				implementCheckMkdir(t, f.home)
			case "worktree inside":
				f.tree = filepath.Join(f.home, "tree")
				implementCheckMkdir(t, f.tree)
			case "equal":
				f.tree = f.home
			case "lstat failure", "nesting stat failure":
				bad := f.home
				if name == "nesting stat failure" {
					bad = f.root
				}
				f.deps.lstat = func(path string) (fs.FileInfo, error) {
					if path == bad {
						return nil, fs.ErrPermission
					}
					return os.Lstat(path)
				}
			}
			calls := 0
			f.deps.version = func(string) ([]byte, error) { calls++; return []byte("1.4.0\n"), nil }
			code, out, _ := f.run()
			if name == "relative" || name == "unclean" {
				if code != 2 || calls != 0 || out != "" {
					t.Fatalf("usage: exit %d, calls %d, stdout %q", code, calls, out)
				}
				return
			}
			if name == "valid" {
				if code != 0 || calls != 1 {
					t.Fatalf("exit %d, version calls %d: %s", code, calls, out)
				}
			} else if code == 0 || !strings.HasPrefix(out, "CHECK:") || calls != 0 {
				t.Fatalf("invalid paths: exit %d, calls %d: %s", code, calls, out)
			}
		})
	}
}

func TestImplementCheckTrustAndUsage(t *testing.T) {
	t.Run("trust first", func(t *testing.T) {
		deps := implementCheckDeps{trust: func() error { return errors.New("untrusted") }}
		var out, stderr bytes.Buffer
		code := implementCheckCommand([]string{"--unknown"}, &out, &stderr, deps)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "untrusted") {
			t.Fatalf("%d %q %q", code, &out, &stderr)
		}
	})
	for _, name := range []string{"missing home", "missing worktree", "missing saved", "missing hash", "empty", "repeat home", "repeat worktree", "repeat saved", "repeat hash", "repeat agy", "unknown", "positional", "hex short", "hex invalid", "relative home", "relative worktree", "relative saved", "relative agy", "unclean agy", "empty agy", "empty HOME", "relative HOME"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			args := f.args()
			indexes := map[string]int{"home": 1, "worktree": 3, "saved": 5, "hash": 7, "agy": 9}
			words := strings.Fields(name)
			switch words[0] {
			case "missing":
				i := indexes[words[1]] - 1
				args = append(args[:i], args[i+2:]...)
			case "repeat":
				i := indexes[words[1]]
				args = append(args, args[i-1], args[i])
			case "empty":
				switch name {
				case "empty HOME":
					f.deps.accountHome = ""
				case "empty agy":
					args[9] = ""
				default:
					args[1] = ""
				}
			case "relative":
				if name == "relative HOME" {
					f.deps.accountHome = "relative"
				} else {
					args[indexes[words[1]]] = "relative"
				}
			case "unclean":
				args[9] += "/.."
			case "unknown":
				args = append(args, "--what")
			case "positional":
				args = append(args, "extra")
			case "hex":
				if name == "hex short" {
					args[7] = "abc"
				} else {
					args[7] = strings.Repeat("z", 64)
				}
			}
			var out, stderr bytes.Buffer
			if code := implementCheckCommand(args, &out, &stderr, f.deps); code != 2 || out.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("exit %d stdout %q stderr %q", code, &out, &stderr)
			}
		})
	}
	t.Run("default agy uses calling HOME", func(t *testing.T) {
		f := implementCheckFixture(t)
		f.deps.version = func(path string) ([]byte, error) {
			if path != filepath.Join(f.account, ".local/bin/agy") {
				t.Errorf("agy %q", path)
			}
			return []byte("1.4.0\n"), nil
		}
		var out, stderr bytes.Buffer
		args := f.args()
		if code := implementCheckCommand(args[:8], &out, &stderr, f.deps); code != 0 {
			t.Fatalf("%d %s %s", code, &out, &stderr)
		}
	})
	t.Run("main dispatch and usage", func(t *testing.T) {
		var out, stderr bytes.Buffer
		if code := run([]string{"implement-check", "--unknown"}, nil, &out, &stderr); code != 1 || strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("routing: %d %s", code, &stderr)
		}
		stderr.Reset()
		if code := run(nil, nil, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "implement-check --home") {
			t.Fatalf("usage: %d %s", code, &stderr)
		}
	})
}

func TestImplementCheckFIFO(t *testing.T) {
	for _, name := range []string{"shared", "settings", "hooks", "mcp", "agent", "saved git", "actual git", "builtin", "builtin list", "trusted settings", "trusted hooks", "trusted agent"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			f.addAgent(t)
			paths := map[string]string{
				"shared":    filepath.Join(f.account, ".gemini/antigravity-cli/settings.json"),
				"settings":  filepath.Join(f.home, ".gemini/antigravity-cli/settings.json"),
				"hooks":     filepath.Join(f.home, ".gemini/config/hooks.json"),
				"mcp":       filepath.Join(f.home, ".gemini/config/mcp_config.json"),
				"agent":     filepath.Join(f.home, ".gemini/config/agents/argon-implementer.md"),
				"saved git": f.saved, "actual git": filepath.Join(f.tree, ".git"),
				"builtin":          filepath.Join(f.home, ".gemini/antigravity-cli/builtin/tool.txt"),
				"builtin list":     filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt"),
				"trusted settings": filepath.Join(f.home, "implementer/settings.json"),
				"trusted hooks":    filepath.Join(f.home, "implementer/hooks.json"),
				"trusted agent":    filepath.Join(f.home, "implementer/argon-implementer.md"),
			}
			path := paths[name]
			implementCheckRemove(t, path)
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			type result struct {
				code int
				out  string
			}
			done := make(chan result, 1)
			go func() { code, out, _ := f.run(); done <- result{code, out} }()
			select {
			case got := <-done:
				if got.code != 1 || !strings.Contains(got.out, fmt.Sprintf("%q", path)) {
					t.Fatalf("FIFO: %d %s", got.code, got.out)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("FIFO blocked implement-check")
			}
		})
	}
}

func TestImplementCheckFilesystemErrors(t *testing.T) {
	for _, name := range []string{"home root", "nested home", "builtin", "agents", "worktree", "lstat", "readlink"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			f.addAgent(t)
			nested := filepath.Join(f.home, "nested")
			implementCheckMkdir(t, nested)
			link := filepath.Join(f.home, "link")
			implementCheckLink(t, "missing", link)
			bad := map[string]string{"home root": f.home, "nested home": nested, "builtin": filepath.Join(f.home, ".gemini/antigravity-cli/builtin"), "agents": filepath.Join(f.home, ".gemini/config/agents"), "worktree": f.tree}[name]
			f.deps.readDir = func(path string) ([]os.DirEntry, error) {
				if path == bad {
					return nil, fs.ErrPermission
				}
				return os.ReadDir(path)
			}
			if name == "lstat" {
				f.deps.lstat = func(path string) (fs.FileInfo, error) {
					if path == link {
						return nil, fs.ErrPermission
					}
					return os.Lstat(path)
				}
			}
			if name == "readlink" {
				f.deps.readlink = func(string) (string, error) { return "", fs.ErrPermission }
			}
			f.deps.git = func(...string) ([]byte, int, error) { return nil, -1, errors.New("later git error") }
			implementCheckWrite(t, filepath.Join(f.home, "implementer/settings.json"), "different")
			code, out, _ := f.run()
			if code != 1 || !strings.Contains(out, "permission denied") || !strings.Contains(out, "home settings.json differs") || !strings.Contains(out, "later git error") {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestImplementCheckSafeRead(t *testing.T) {
	for _, name := range []string{"missing", "directory", "symlink", "symlink parent", "oversized", "regular"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.root, "read")
			switch name {
			case "directory":
				implementCheckMkdir(t, path)
			case "symlink":
				implementCheckLink(t, f.saved, path)
			case "symlink parent":
				implementCheckLink(t, f.tree, path)
				path = filepath.Join(path, ".git")
			case "oversized":
				implementCheckWrite(t, path, strings.Repeat("a", 65))
			case "regular":
				implementCheckWrite(t, path, "ok")
			}
			data, err := implementRead(path, 64, false)
			if name == "regular" {
				if err != nil || string(data) != "ok" {
					t.Fatalf("%q %v", data, err)
				}
			} else if err == nil {
				t.Fatal("unsafe read passed")
			}
		})
	}
}

func TestImplementCheckMCP(t *testing.T) {
	for _, name := range []string{"missing", "directory", "symlink", "FIFO", "nonempty untrusted", "nonempty no trusted copy", "nonempty trusted", "empty"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.home, ".gemini/config/mcp_config.json")
			trusted := filepath.Join(f.home, "implementer/mcp_config.json")
			implementCheckRemove(t, path)
			switch name {
			case "directory":
				implementCheckMkdir(t, path)
			case "symlink":
				implementCheckLink(t, f.saved, path)
			case "FIFO":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "nonempty untrusted":
				implementCheckWrite(t, path, "untrusted feature on")
				implementCheckWrite(t, trusted, "trusted feature on")
			case "nonempty no trusted copy":
				implementCheckWrite(t, path, "feature on")
				implementCheckRemove(t, trusted)
			case "nonempty trusted":
				implementCheckWrite(t, path, "feature on")
				implementCheckWrite(t, trusted, "feature on")
			case "empty":
				implementCheckWrite(t, path, "")
			}
			code, out, _ := f.run()
			want := 1
			if name == "empty" {
				want = 0
			}
			if code != want || want == 1 && !strings.Contains(out, "mcp_config.json") {
				t.Fatalf("exit %d: %s", code, out)
			}
			if strings.HasPrefix(name, "nonempty") && !strings.Contains(out, "feature-on state must be checked by #1432's contract, not here") {
				t.Fatalf("missing feature-on contract explanation: %s", out)
			}
		})
	}
}

func TestImplementCheckSharedAndComparisons(t *testing.T) {
	for _, name := range []string{"hash differs", "missing", "directory", "settings differ", "hooks differ", "equal", "uppercase", "symlink followed"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			shared := filepath.Join(f.account, ".gemini/antigravity-cli/settings.json")
			switch name {
			case "hash differs":
				implementCheckWrite(t, shared, "changed")
			case "missing":
				implementCheckRemove(t, shared)
			case "directory":
				implementCheckRemove(t, shared)
				implementCheckMkdir(t, shared)
			case "settings differ":
				implementCheckWrite(t, filepath.Join(f.home, ".gemini/antigravity-cli/settings.json"), "changed")
			case "hooks differ":
				implementCheckWrite(t, filepath.Join(f.home, ".gemini/config/hooks.json"), "changed")
			case "uppercase":
				f.hash = strings.ToUpper(f.hash)
			case "symlink followed":
				target := filepath.Join(f.account, "shared-target")
				implementCheckWrite(t, target, "shared")
				implementCheckRemove(t, shared)
				implementCheckLink(t, target, shared)
			}
			code, out, _ := f.run()
			want := 1
			if name == "equal" || name == "uppercase" || name == "symlink followed" {
				want = 0
			}
			if code != want {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestImplementCheckQuotedNames(t *testing.T) {
	for _, home := range []bool{true, false} {
		t.Run(fmt.Sprint(home), func(t *testing.T) {
			f := implementCheckFixture(t)
			root := f.tree
			if home {
				root = f.home
			}
			for _, name := range []string{" space ", "line\nCHECK: fake", "newline\n", "CHECK: fake"} {
				implementCheckLink(t, "target\nCHECK: fake", filepath.Join(root, name))
			}
			code, out, _ := f.run()
			if code != 1 {
				t.Fatal("symlinks passed")
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if len(lines) != 4 {
				t.Fatalf("forged/missing lines: %q", out)
			}
			for _, line := range lines {
				if !strings.HasPrefix(line, "CHECK: symlink in ") {
					t.Fatalf("forged line: %q", line)
				}
			}
			for _, name := range []string{" space ", "line\nCHECK: fake", "newline\n", "CHECK: fake"} {
				if !strings.Contains(out, fmt.Sprintf("%q", filepath.Join(root, name))) {
					t.Fatalf("name missing: %q", out)
				}
			}
		})
	}
}

func TestImplementCheckCliLog(t *testing.T) {
	for _, name := range []string{"pattern", "missing target", "symlink target", "directory target", "elsewhere", "other symlink", "valid"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			dir := filepath.Join(f.home, ".gemini/antigravity-cli")
			target := "log/cli-20261019_123456.log"
			path := filepath.Join(dir, target)
			implementCheckWrite(t, path, "log")
			switch name {
			case "pattern":
				target = "log/cli-wrong.log"
			case "missing target":
				implementCheckRemove(t, path)
			case "symlink target":
				implementCheckRemove(t, path)
				implementCheckLink(t, f.saved, path)
			case "directory target":
				implementCheckRemove(t, path)
				implementCheckMkdir(t, path)
			case "elsewhere":
				dir = f.home
			case "other symlink":
				target = "settings.json"
			}
			implementCheckLink(t, target, filepath.Join(dir, "cli.log"))
			code, out, _ := f.run()
			want := 1
			if name == "valid" {
				want = 0
			}
			if code != want {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestImplementCheckRefusedNames(t *testing.T) {
	for _, home := range []bool{true, false} {
		names := append([]string{}, implementRefusedNames...)
		if home {
			names = append(names, refusedCustomisation...)
			names = append(names, "mcp_config.json")
		}
		for _, name := range names {
			t.Run(fmt.Sprintf("home=%t/%s", home, name), func(t *testing.T) {
				f := implementCheckFixture(t)
				root := f.tree
				if home {
					root = f.home
				}
				path := filepath.Join(root, "nested", strings.ToUpper(name[:len(name)/2])+name[len(name)/2:])
				implementCheckWrite(t, path, "refused")
				code, out, _ := f.run()
				if code != 1 || !strings.Contains(out, "refused name") || !strings.Contains(out, fmt.Sprintf("%q", path)) {
					t.Fatalf("exit %d: %s", code, out)
				}
			})
		}
	}
	for _, rel := range []string{"nested/.gemini", "nested/hooks.json", ".gemini/config/skills"} {
		t.Run(rel, func(t *testing.T) {
			f := implementCheckFixture(t)
			implementCheckMkdir(t, filepath.Join(f.home, rel))
			code, out, _ := f.run()
			if code != 1 || !strings.Contains(out, "refused name in home") {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
	t.Run("home exemptions", func(t *testing.T) {
		f := implementCheckFixture(t)
		f.addAgent(t)
		for _, rel := range []string{"implementer/skills/agents.md", "guard/.git/CLAUDE.md"} {
			implementCheckWrite(t, filepath.Join(f.home, rel), "trusted")
		}
		// Builtin contents are exempt from names but still checked by their list.
		builtin := filepath.Join(f.home, ".gemini/antigravity-cli/builtin")
		implementCheckWrite(t, filepath.Join(builtin, "skills/AGENTS.md"), "trusted")
		list := fmt.Sprintf("%x  ./tool.txt\n%x  ./skills/AGENTS.md\n", sha256.Sum256([]byte("tool")), sha256.Sum256([]byte("trusted")))
		implementCheckWrite(t, filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt"), list)
		code, out, _ := f.run()
		if code != 0 {
			t.Fatalf("%d %s", code, out)
		}
	})
	t.Run("worktree baseline names", func(t *testing.T) {
		f := implementCheckFixture(t)
		for _, rel := range []string{"AGENTS.md", "CLAUDE.md", ".gitattributes", ".gitmodules", ".github/deep/hooks.json"} {
			implementCheckWrite(t, filepath.Join(f.tree, rel), "baseline")
		}
		var out bytes.Buffer
		c := implementChecker{deps: f.deps, out: &out}
		c.scan(f.tree, false)
		if c.problems != 0 {
			t.Fatal(out.String())
		}
		implementCheckWrite(t, filepath.Join(f.tree, ".github/deep/.git/hooks.json"), "refused")
		c.scan(f.tree, false)
		if c.problems != 1 || !strings.Contains(out.String(), "refused name in worktree") {
			t.Fatal(out.String())
		}
	})
	t.Run("prune refused directories", func(t *testing.T) {
		f := implementCheckFixture(t)
		dir := filepath.Join(f.home, "nested/skills")
		implementCheckWrite(t, filepath.Join(dir, "agents.md"), "refused")
		f.deps.readDir = func(path string) ([]os.DirEntry, error) {
			if path == dir {
				t.Error("descended into refused directory")
			}
			return os.ReadDir(path)
		}
		code, out, _ := f.run()
		if code != 1 || strings.Count(out, "CHECK:") != 1 {
			t.Fatalf("%d %s", code, out)
		}
	})
}

func TestImplementCheckBuiltinListErrors(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, tc := range []struct{ name, data string }{
		{"empty", ""}, {"no final newline", hash + "  ./file"}, {"short hash", "abc  ./file\n"},
		{"uppercase", strings.Repeat("A", 64) + "  ./file\n"}, {"one space", hash + " ./file\n"},
		{"missing dot slash", hash + "  file\n"}, {"dotdot", hash + "  ./a/../b\n"},
		{"dot", "LINK ./a/./b\n"}, {"absolute", "LINK .//a\n"}, {"empty component", "LINK ./a//b\n"},
		{"NUL", "LINK ./a\x00b\n"}, {"backslash", "LINK ./a\\b\n"}, {"empty path", "LINK ./\n"},
		{"duplicate", "LINK ./a\nLINK ./a\n"}, {"file and LINK", hash + "  ./a\nLINK ./a\n"},
		{"garbage", "garbage\n"}, {"line two", hash + "  ./file\ngarbage\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckFixture(t)
			implementCheckWrite(t, filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt"), tc.data)
			implementCheckWrite(t, filepath.Join(f.home, ".gemini/antigravity-cli/builtin/extra"), "extra")
			code, out, _ := f.run()
			if code != 1 || !strings.Contains(out, "untrusted builtin/ hash list") || strings.Contains(out, "extra builtin/") {
				t.Fatalf("%d %s", code, out)
			}
			if tc.name == "line two" && !strings.Contains(out, "line 2") {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckBuiltin(t *testing.T) {
	for _, name := range []string{"no list", "no builtin", "builtin is file", "extra", "missing", "changed", "read cap", "FIFO", "socket", "kind differs", "recorded link", "read error", "version failed", "version timeout", "version junk", "empty directory", "nested file", "read cap boundary", "valid"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			dir := filepath.Join(f.home, ".gemini/antigravity-cli/builtin")
			file := filepath.Join(dir, "tool.txt")
			list := filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt")
			switch name {
			case "no list":
				f.deps.version = func(string) ([]byte, error) { return []byte("9.9\n"), nil }
			case "no builtin":
				implementCheckRemove(t, dir)
			case "builtin is file":
				implementCheckRemove(t, dir)
				implementCheckWrite(t, dir, "bad")
			case "extra":
				implementCheckWrite(t, filepath.Join(dir, "extra"), "extra")
			case "missing":
				implementCheckRemove(t, file)
			case "changed":
				implementCheckWrite(t, file, "changed")
			case "read cap", "read cap boundary":
				size := 1 << 20
				if name == "read cap" {
					size++
				}
				data := strings.Repeat("a", size)
				implementCheckWrite(t, file, data)
				implementCheckWrite(t, list, fmt.Sprintf("%x  ./tool.txt\n", sha256.Sum256([]byte(data))))
			case "FIFO":
				if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				t.Chdir(dir) // Keep the Unix socket address below the platform limit.
				listener, err := net.Listen("unix", "socket")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = listener.Close() }()
			case "kind differs":
				implementCheckWrite(t, list, "LINK ./tool.txt\n")
			case "recorded link":
				implementCheckRemove(t, file)
				implementCheckLink(t, "missing", file)
				implementCheckWrite(t, list, "LINK ./tool.txt\n")
			case "read error":
				implementCheckRemove(t, file)
				implementCheckLink(t, f.saved, file) // Simulate a replacement after Lstat; the descriptor open must refuse it.
				f.deps.lstat = func(path string) (fs.FileInfo, error) {
					if path == file {
						return os.Stat(path)
					}
					return os.Lstat(path)
				}
			case "version failed":
				f.deps.version = func(string) ([]byte, error) { return nil, errors.New("exit status 1") }
			case "version timeout":
				f.deps.version = func(string) ([]byte, error) { return nil, context.DeadlineExceeded }
			case "version junk":
				f.deps.version = func(string) ([]byte, error) { return []byte("junk\n"), nil }
			case "empty directory":
				implementCheckMkdir(t, filepath.Join(dir, "empty"))
			case "nested file":
				implementCheckWrite(t, filepath.Join(dir, "nested/file"), "nested")
				implementCheckWrite(t, list, fmt.Sprintf("%x  ./tool.txt\n%x  ./nested/file\n", sha256.Sum256([]byte("tool")), sha256.Sum256([]byte("nested"))))
			}
			code, out, _ := f.run()
			want := 1
			if name == "valid" || name == "empty directory" || name == "nested file" || name == "read cap boundary" {
				want = 0
			}
			if code != want {
				t.Fatalf("%d %s", code, out)
			}
			if name == "read cap" && !strings.Contains(out, fmt.Sprintf("builtin file %q is larger than the 1 MiB read cap; builtin files are hashed whole, so a larger file needs a code change", file)) {
				t.Fatalf("missing builtin read-cap explanation: %s", out)
			}
			if (name == "FIFO" || name == "socket") && !strings.Contains(out, "unknown file type") {
				t.Fatal(out)
			}
			if name == "recorded link" && (strings.Contains(out, "builtin/ entry differs") || !strings.Contains(out, "symlink in home")) {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckVersion(t *testing.T) {
	for _, tc := range []struct{ name, out, want string }{
		{"newline", "1.4.2\n", "1.4.2"}, {"plain", "1.4.2", "1.4.2"},
		{"two components", "1.4", "1.4"}, {"leading zeros", "01.02", "01.02"},
		{"prerelease", "agy 1.4.0-beta", ""}, {"embedded junk", "1x.4", ""},
		{"prefix", "v1.4.2", ""}, {"build suffix", "1.4.2 (build)", ""},
		{"empty", "", ""}, {"empty first line", "\n1.4.2", ""},
		{"extra line", "1.4.2\nextra", ""}, {"extra newline", "1.4.2\n\n", ""},
		{"empty component", "1..2", ""}, {"leading dot", ".1.2", ""},
		{"trailing dot", "1.4.2.", ""}, {"single component", "1", ""},
		{"junk", "junk", ""}, {"trailing space", "1.4.2 ", ""},
		{"leading space", " 1.4.2", ""}, {"carriage return", "1.4.2\r\n", ""},
		{"hostile", "\x1b\x00\"\\\nCHECK: forged", ""},
		{"long hostile", strings.Repeat("\x1b\x00\"\\\nCHECK: forged", 1<<16), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := implementVersion([]byte(tc.out))
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q %v", got, err)
			}
			if err != nil {
				if len(err.Error()) > 320 || strings.ContainsAny(err.Error(), "\n\r\x1b\x00") {
					t.Fatalf("unbounded or unquoted error: %q", err)
				}
				if len(tc.out) <= 64 && err.Error() != fmt.Sprintf("invalid agy version %q", tc.out) {
					t.Fatalf("error does not quote output: %v", err)
				}
				if len(tc.out) > 64 && !strings.Contains(err.Error(), "(truncated)") {
					t.Fatalf("missing truncation notice: %v", err)
				}
			}
		})
	}
}

func TestImplementCheckBuiltinRefusesVersion(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"prerelease", "agy 1.4.0-beta\n"}, {"embedded junk", "1x.4"},
		{"prefix", "v1.4.0\n"}, {"extra line", "1.4.0\nextra"},
		{"single component", "1"},
		{"long hostile", strings.Repeat("\x1b\x00\"\\\nCHECK: forged", 1<<16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckFixture(t)
			f.deps.version = func(string) ([]byte, error) { return []byte(tc.out), nil }
			code, out, stderr := f.run()
			if code != 1 || stderr != "" || !strings.Contains(out, "version from") ||
				strings.Contains(out, "implement-check: ok") || strings.Contains(out, "no trusted builtin/ hash list") {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
			}
			// The fixture has a matching 1.4.0 list and builtin directory. Invalid
			// versions must stop before list lookup, including when no list exists.
			implementCheckRemove(t, filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt"))
			_, withoutList, _ := f.run()
			if withoutList != out {
				t.Fatalf("list presence changed refusal: %s", withoutList)
			}
			if len(out) > 1024+len(filepath.Join(f.root, "fake-agy")) || strings.Count(out, "\n") != 1 ||
				strings.ContainsAny(out, "\r\x1b\x00") {
				t.Fatalf("unbounded or unquoted command error: %q", out)
			}
		})
	}
}

func TestImplementCheckAgent(t *testing.T) {
	for _, name := range []string{"modified", "extra", "dotfile", "subdirectory", "symlink file", "wrong name", "directory is file", "absent", "exact"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			dir := filepath.Join(f.home, ".gemini/config/agents")
			file := filepath.Join(dir, "argon-implementer.md")
			if name != "absent" {
				f.addAgent(t)
			}
			switch name {
			case "modified":
				implementCheckWrite(t, file, "changed")
			case "extra":
				implementCheckWrite(t, filepath.Join(dir, "extra"), "extra")
			case "dotfile":
				implementCheckWrite(t, filepath.Join(dir, ".hidden"), "extra")
			case "subdirectory":
				implementCheckMkdir(t, filepath.Join(dir, "sub"))
			case "symlink file":
				implementCheckRemove(t, file)
				implementCheckLink(t, filepath.Join(f.home, "implementer/argon-implementer.md"), file)
			case "wrong name":
				implementCheckRemove(t, file)
				implementCheckWrite(t, filepath.Join(dir, "wrong.md"), "agent")
			case "directory is file":
				implementCheckRemove(t, dir)
				implementCheckWrite(t, dir, "file")
			}
			code, out, _ := f.run()
			want := 1
			if name == "absent" || name == "exact" {
				want = 0
			}
			if code != want {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
}

func TestImplementCheckWorktree(t *testing.T) {
	for _, name := range []string{"git changed", "git missing", "git directory", "git oversized", "saved oversized", "deep symlink", "valid"} {
		t.Run(name, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.tree, ".git")
			switch name {
			case "git changed":
				implementCheckWrite(t, path, "changed")
			case "git missing":
				implementCheckRemove(t, path)
			case "git directory":
				implementCheckRemove(t, path)
				implementCheckMkdir(t, path)
			case "git oversized":
				implementCheckWrite(t, path, strings.Repeat("a", (64<<10)+1))
			case "saved oversized":
				implementCheckWrite(t, f.saved, strings.Repeat("a", (64<<10)+1))
			case "deep symlink":
				implementCheckMkdir(t, filepath.Join(f.tree, "sub/deep"))
				implementCheckLink(t, "missing", filepath.Join(f.tree, "sub/deep/link"))
			}
			calls := 0
			git := f.deps.git
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls++
				return git(args...)
			}
			code, out, _ := f.run()
			want := 1
			if name == "valid" {
				want = 0
			}
			wantGit := name == "valid" || name == "deep symlink"
			if code != want || (calls != 0) != wantGit {
				t.Fatalf("git calls %d, exit %d: %s", calls, code, out)
			}
		})
	}
	t.Run("do not traverse top level git", func(t *testing.T) {
		f := implementCheckFixture(t)
		implementCheckRemove(t, filepath.Join(f.tree, ".git"))
		implementCheckWrite(t, filepath.Join(f.tree, ".git/agents.md"), "ignored by walk")
		var out bytes.Buffer
		c := implementChecker{deps: f.deps, out: &out}
		c.scan(f.tree, false)
		if c.problems != 0 {
			t.Fatal(out.String())
		}
	})
}

func TestImplementCheckProblemCapAndOutput(t *testing.T) {
	t.Run("cap still counts all checks", func(t *testing.T) {
		f := implementCheckFixture(t)
		for i := range 205 {
			implementCheckLink(t, "missing", filepath.Join(f.home, fmt.Sprintf("link-%03d", i)))
		}
		implementCheckWrite(t, filepath.Join(f.tree, ".git"), "changed")
		code, out, _ := f.run()
		if code != 1 || strings.Count(out, "\n") != 201 || !strings.HasSuffix(out, "CHECK: and 6 more problems\n") {
			t.Fatalf("%d %s", code, out)
		}
	})
	for _, writer := range []interface{ Write([]byte) (int, error) }{brokenWriter{0}, implementGuardShortWriter{}} {
		f := implementCheckFixture(t)
		if code := implementCheckCommand(f.args(), writer, &bytes.Buffer{}, f.deps); code != 1 {
			t.Fatalf("undelivered success exited %d", code)
		}
	}
}

func TestImplementCheckExemptionsStillRefuseSymlinks(t *testing.T) {
	for _, tc := range []struct {
		home bool
		rel  string
	}{
		{true, "implementer/.codex"}, {true, "guard/.claude"},
		{true, ".gemini/antigravity-cli/builtin/agents.md"},
		{false, ".github/deep/AGENTS.md"},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			f := implementCheckFixture(t)
			root := f.tree
			if tc.home {
				root = f.home
			}
			path := filepath.Join(root, tc.rel)
			implementCheckMkdir(t, filepath.Dir(path))
			implementCheckLink(t, "missing", path)
			var out bytes.Buffer
			c := implementChecker{deps: f.deps, out: &out}
			c.scan(root, tc.home)
			if c.problems != 1 || !strings.Contains(out.String(), "symlink in") || strings.Contains(out.String(), "refused name") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestImplementCheckPartialDirectoryRead(t *testing.T) {
	f := implementCheckFixture(t)
	path := filepath.Join(f.home, "link")
	implementCheckLink(t, "missing", path)
	f.deps.readDir = func(dir string) ([]os.DirEntry, error) {
		entries, err := os.ReadDir(dir)
		if dir == f.home {
			return entries, fs.ErrPermission
		}
		return entries, err
	}
	code, out, _ := f.run()
	if code != 1 || !strings.Contains(out, "permission denied") || !strings.Contains(out, "symlink in home") {
		t.Fatalf("%d %s", code, out)
	}
}

type implementCheckTestFixture struct {
	root, home, tree, saved, account, hash string
	deps                                   implementCheckDeps
}

func implementCheckFixture(t *testing.T) *implementCheckTestFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &implementCheckTestFixture{root: root, home: filepath.Join(root, "home"), tree: filepath.Join(root, "tree"), saved: filepath.Join(root, "saved-git"), account: filepath.Join(root, "account")}
	for _, path := range []string{f.home, f.tree, f.account} {
		implementCheckMkdir(t, path)
	}
	f.deps = implementCheckDefaults()
	f.deps.trust = func() error { return nil }
	f.deps.accountHome = f.account
	f.deps.version = func(string) ([]byte, error) { return []byte("1.4.0\n"), nil }
	f.deps.git = func(args ...string) ([]byte, int, error) {
		if args[0] == "merge-base" {
			return []byte(strings.Repeat("a", 40) + "\n"), 0, nil
		}
		if args[0] != "ls-tree" && args[0] != "diff-tree" && args[0] != "ls-files" && args[0] != "diff" {
			t.Errorf("unexpected git %v", args)
		}
		return nil, 0, nil
	}
	for path, data := range map[string]string{
		filepath.Join(f.account, ".gemini/antigravity-cli/settings.json"): "shared",
		filepath.Join(f.home, ".gemini/antigravity-cli/settings.json"):    "settings",
		filepath.Join(f.home, "implementer/settings.json"):                "settings",
		filepath.Join(f.home, ".gemini/config/hooks.json"):                "hooks",
		filepath.Join(f.home, "implementer/hooks.json"):                   "hooks",
		filepath.Join(f.home, ".gemini/config/mcp_config.json"):           "",
		filepath.Join(f.home, ".gemini/antigravity-cli/builtin/tool.txt"): "tool",
		filepath.Join(f.home, "implementer/builtin.agy-1.4.0.txt"):        fmt.Sprintf("%x  ./tool.txt\n", sha256.Sum256([]byte("tool"))),
		filepath.Join(f.tree, ".git"):                                     "gitdir: fixture\n", f.saved: "gitdir: fixture\n",
	} {
		implementCheckWrite(t, path, data)
	}
	f.hash = fmt.Sprintf("%x", sha256.Sum256([]byte("shared")))
	return f
}
func (f *implementCheckTestFixture) addAgent(t *testing.T) {
	t.Helper()
	implementCheckWrite(t, filepath.Join(f.home, ".gemini/config/agents/argon-implementer.md"), "agent")
	implementCheckWrite(t, filepath.Join(f.home, "implementer/argon-implementer.md"), "agent")
}
func (f *implementCheckTestFixture) args() []string {
	return []string{"--home", f.home, "--worktree", f.tree, "--git-file", f.saved, "--shared-settings-sha256", f.hash, "--agy", filepath.Join(f.root, "fake-agy")}
}
func (f *implementCheckTestFixture) run() (int, string, string) {
	var out, stderr bytes.Buffer
	code := implementCheckCommand(f.args(), &out, &stderr, f.deps)
	return code, out.String(), stderr.String()
}
func implementCheckWrite(t *testing.T, path, data string) {
	t.Helper()
	implementCheckMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func implementCheckMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func implementCheckRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}
func implementCheckLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
