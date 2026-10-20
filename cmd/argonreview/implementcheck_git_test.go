package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestImplementCheckGitFailures(t *testing.T) {
	for _, stage := range []string{"merge-base", "ls-tree", "cat-file", "diff", "ls-files"} {
		for _, outcome := range []struct {
			name   string
			out    []byte
			status int
			err    error
		}{
			{name: "failed start", status: -1, err: fs.ErrNotExist},
			{name: "signal", status: -1, err: errors.New("signal: killed")},
			{name: "timeout", status: -1, err: context.DeadlineExceeded},
			{name: "unreadable output", status: -1, err: io.ErrUnexpectedEOF},
			{name: "oversized output", out: bytes.Repeat([]byte("a"), (1<<20)+1)},
			{name: "output copy failure", status: -1, err: io.ErrClosedPipe},
			{name: "malformed output", out: []byte("garbage")},
			{name: "status 2", status: 2}, {name: "status 128", status: 128},
		} {
			t.Run(stage+"/"+outcome.name, func(t *testing.T) {
				f := implementCheckFixture(t)
				hit, probedAbsent := false, false
				f.deps.lstat = func(path string) (fs.FileInfo, error) {
					if path == filepath.Join(f.tree, ".github") {
						probedAbsent = true
					}
					return os.Lstat(path)
				}
				f.deps.git = func(args ...string) ([]byte, int, error) {
					if args[0] == stage && (stage == "merge-base" || stage == "cat-file" || args[len(args)-1] == ".github") {
						hit = true
						return outcome.out, outcome.status, outcome.err
					}
					if args[0] == "merge-base" {
						return []byte(strings.Repeat("a", 40) + "\n"), 0, nil
					}
					if args[0] == "ls-tree" && args[len(args)-1] == ".github" {
						return []byte("040000 tree " + strings.Repeat("b", 40) + "\t.github\x00"), 0, nil
					}
					if args[0] == "cat-file" {
						return []byte("tree\n"), 0, nil
					}
					if stage == "merge-base" {
						t.Errorf("git called after failed merge-base: %v", args)
					}
					return nil, 0, nil
				}
				var out bytes.Buffer
				c := implementChecker{deps: f.deps, out: &out}
				c.baseline(f.tree)
				if !hit || c.problems == 0 || probedAbsent {
					t.Fatalf("hit %t, absent probe %t, problems %d: %s", hit, probedAbsent, c.problems, &out)
				}
				if stage == "ls-tree" && !strings.Contains(out.String(), `cannot tell whether ".github" is tracked`) {
					t.Fatal(out.String())
				}
			})
		}
	}
	for _, stage := range []string{"merge-base", "ls-tree", "cat-file", "diff", "ls-files"} {
		t.Run(stage+"/status 1", func(t *testing.T) {
			f := implementCheckFixture(t)
			calls := 0
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls++
				if args[0] == stage {
					return nil, 1, nil
				}
				if args[0] == "merge-base" {
					return []byte(strings.Repeat("a", 40) + "\n"), 0, nil
				}
				if args[0] == "ls-tree" {
					return []byte("100644 blob " + strings.Repeat("b", 40) + "\t" + args[len(args)-1] + "\x00"), 0, nil
				}
				if args[0] == "cat-file" {
					return []byte("blob\n"), 0, nil
				}
				return nil, 0, nil
			}
			var out bytes.Buffer
			c := implementChecker{deps: f.deps, out: &out}
			c.baseline(f.tree)
			if c.problems == 0 || stage == "merge-base" && (calls != 1 || out.String() != "CHECK: no merge base with origin/main\n") {
				t.Fatalf("calls %d: %s", calls, &out)
			}
		})
	}
}

func TestImplementCheckGitAbsenceAndPresence(t *testing.T) {
	for _, mode := range []string{"absent exists", "absent stat error", "tracked changed", "tracked untracked", "absent", "tracked"} {
		t.Run(mode, func(t *testing.T) {
			f := implementCheckFixture(t)
			if mode == "absent exists" {
				implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "new")
			}
			if mode == "absent stat error" {
				f.deps.lstat = func(path string) (fs.FileInfo, error) {
					if filepath.Base(path) == "AGENTS.md" {
						return nil, fs.ErrPermission
					}
					return os.Lstat(path)
				}
			}
			calls := []string{}
			mb := strings.Repeat("a", 64)
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls = append(calls, strings.Join(args, " "))
				switch args[0] {
				case "merge-base":
					if strings.Join(args, " ") != "merge-base HEAD origin/main" {
						t.Errorf("args %v", args)
					}
					return []byte(mb + "\n"), 0, nil
				case "ls-tree":
					base := args[len(args)-1]
					if strings.Join(args, " ") != "ls-tree -z "+mb+" -- "+base {
						t.Errorf("args %v", args)
					}
					if strings.HasPrefix(mode, "tracked") {
						return []byte("100644 blob " + strings.Repeat("b", 64) + "\t" + base + "\x00"), 0, nil
					}
				case "diff":
					if strings.Join(args, " ") != "diff --quiet --no-ext-diff --no-textconv "+mb+" -- "+args[len(args)-1] {
						t.Errorf("args %v", args)
					}
					if mode == "tracked changed" {
						return nil, 1, nil
					}
				case "cat-file":
					if strings.Join(args, " ") != "cat-file -t "+strings.Repeat("b", 64) {
						t.Errorf("args %v", args)
					}
					return []byte("blob\n"), 0, nil
				case "ls-files":
					if strings.Join(args, " ") != "ls-files --others -z -- "+args[len(args)-1] {
						t.Errorf("args %v", args)
					}
					if mode == "tracked untracked" {
						return []byte("extra\x00"), 0, nil
					}
				default:
					t.Errorf("unexpected args %v", args)
				}
				return nil, 0, nil
			}
			var out bytes.Buffer
			c := implementChecker{deps: f.deps, out: &out}
			c.baseline(f.tree)
			good := mode == "absent" || mode == "tracked"
			if (c.problems == 0) != good {
				t.Fatalf("%s", &out)
			}
			want := 6
			if strings.HasPrefix(mode, "tracked") {
				want = 21
			}
			if len(calls) != want {
				t.Fatalf("calls %v", calls)
			}
		})
	}
}

func TestImplementCheckTreeEntry(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, data := range []string{
		"100644 blob " + oid + "\twrong\x00", "100644 blob short\t.github\x00",
		"100644 blob " + oid + "\t.github", "100644  blob " + oid + "\t.github\x00",
		"garbage", "100644 tree " + oid + "\t.github\x00", "040000 blob " + oid + "\t.github\x00",
		"040000 tree " + oid + "\t.github\x00extra\x00",
	} {
		if implementTreeEntry([]byte(data), ".github") {
			t.Errorf("accepted %q", data)
		}
	}
	for _, mode := range []string{"040000 tree", "100644 blob", "100755 blob", "120000 blob", "160000 commit"} {
		if !implementTreeEntry([]byte(mode+" "+oid+"\t.github\x00"), ".github") {
			t.Errorf("refused %s", mode)
		}
	}
}

func TestImplementCheckProcessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		limit        time.Duration
		status       int
		fail         bool
	}{
		{"failed start", "", time.Second, -1, true},
		{"signal", "kill -KILL $$", time.Second, -1, true},
		{"timeout", "while :; do :; done", 20 * time.Millisecond, -1, true},
		{"output cap", "s=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; i=0; while [ \"$i\" -lt 17000 ]; do printf %s \"$s\"; i=$((i+1)); done", 5 * time.Second, -1, true},
		{"exit 128", "exit 128", time.Second, 128, false},
		{"success", "printf 'ok\\n'", time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.root, "process")
			if tc.script != "" {
				implementCheckWrite(t, path, "#!/bin/sh\n"+tc.script+"\n")
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			out, status, err := implementProcess(path, nil, []string{"PATH=/usr/bin:/bin"}, tc.limit)
			if status != tc.status || (err != nil) != tc.fail {
				t.Fatalf("status %d, error %v, output len %d", status, err, len(out))
			}
		})
	}
	t.Run("writer rejects overflow", func(t *testing.T) {
		var out implementOutput
		if _, err := out.Write(make([]byte, 1<<20)); err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write([]byte("x")); err == nil {
			t.Fatal("overflow passed")
		}
	})
}

func TestImplementCheckVersionRunner(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		fail         bool
	}{
		{"nonzero", "printf '1.4.0\\n'; exit 1", true},
		{"signal", "kill -KILL $$", true},
		{"missing", "", true},
		{"junk", "printf 'junk\\n'", false},
		{"valid", "[ \"$1\" = --version ] || exit 4; [ \"$IMPLEMENT_CHECK_VERSION_TEST\" = inherited ] || exit 6; read unused && exit 5; printf '1.4.0\\n'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.root, "fake-agy")
			if tc.script != "" {
				implementCheckWrite(t, path, "#!/bin/sh\n"+tc.script+"\n")
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("IMPLEMENT_CHECK_VERSION_TEST", "inherited")
			out, err := implementCheckDefaults().version(path)
			if (err != nil) != tc.fail {
				t.Fatalf("%q %v", out, err)
			}
			if tc.name == "valid" && string(out) != "1.4.0\n" {
				t.Fatalf("%q", out)
			}
		})
	}
}

func TestImplementCheckBuiltinVersionRunner(t *testing.T) {
	for _, version := range []string{"1.4.0", "agy 1.4.0-beta"} {
		t.Run(version, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.root, "fake-agy")
			implementCheckWrite(t, path, "#!/bin/sh\nprintf '%s\\n' '"+version+"'\n")
			if err := os.Chmod(path, 0700); err != nil {
				t.Fatal(err)
			}
			f.deps.version = implementCheckDefaults().version
			code, out, stderr := f.run()
			if version == "1.4.0" {
				if code != 0 || out != "implement-check: ok\n" || stderr != "" {
					t.Fatalf("exit %d: %s %s", code, out, stderr)
				}
			} else if code != 1 || !strings.Contains(out, "version from") ||
				strings.Contains(out, "implement-check: ok") || strings.Contains(out, "no trusted builtin/ hash list") {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestImplementCheckGitEnv(t *testing.T) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_NO_LAZY_FETCH", "GIT_NO_REPLACE_OBJECTS", "GIT_OPTIONAL_LOCKS", "PATH", "HOME"} {
		t.Setenv(name, "hostile")
	}
	want := []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"}
	if got := implementGitEnv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("git environment %q; want %q", got, want)
	}
}

func TestImplementCheckGitNoLazyFetch(t *testing.T) {
	f, repo := implementCheckGitFixture(t, true)
	oid := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", "HEAD:AGENTS.md"))
	marker, helper := filepath.Join(f.root, "fetched"), filepath.Join(f.root, "ssh-helper")
	implementCheckWrite(t, helper, "#!/bin/sh\nprintf ran > '"+marker+"'\nexit 1\n")
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"},
		{"remote.origin.promisor", "true"}, {"remote.origin.url", "ssh://fixture.invalid/repo"},
		{"core.sshCommand", helper},
	} {
		implementCheckGitOK(t, repo, "config", pair[0], pair[1])
	}
	implementCheckRemove(t, filepath.Join(repo, ".git/objects", oid[:2], oid[2:]))
	_, status, err := implementGit(f.tree)("cat-file", "-t", oid)
	if err != nil || status == 0 {
		t.Fatalf("missing blob: status %d, error %v", status, err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lazy fetch ran configured program: %v", err)
	}
	// Prove this local fixture reaches the configured program without the
	// guard; the helper exits before any network connection can be made.
	var env []string
	for _, entry := range implementGitEnv() {
		if entry != "GIT_NO_LAZY_FETCH=1" {
			env = append(env, entry)
		}
	}
	_, status, err = implementProcess("/usr/bin/git", []string{"-C", f.tree, "cat-file", "-t", oid}, env, 10*time.Second)
	if err != nil || status == 0 {
		t.Fatalf("control: status %d, error %v", status, err)
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("control did not run configured program: %v", err)
	}
}

func TestImplementCheckRealGitBaseline(t *testing.T) {
	for _, mode := range []string{"modified", "deleted", "untracked github", "absent AGENTS", "new CLAUDE", "corrupt object", "identical"} {
		t.Run(mode, func(t *testing.T) {
			f, repo := implementCheckGitFixture(t, mode != "absent AGENTS")
			switch mode {
			case "modified":
				implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "modified")
			case "deleted":
				implementCheckRemove(t, filepath.Join(f.tree, "AGENTS.md"))
			case "untracked github":
				implementCheckWrite(t, filepath.Join(f.tree, ".github/untracked"), "new")
			case "absent AGENTS":
				implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "new")
			case "new CLAUDE":
				implementCheckWrite(t, filepath.Join(f.tree, "CLAUDE.md"), "new")
			case "corrupt object":
				oid := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", "HEAD:AGENTS.md"))
				implementCheckRemove(t, filepath.Join(repo, ".git/objects", oid[:2], oid[2:]))
			}
			code, out, stderr := f.run()
			want := 1
			if mode == "identical" {
				want = 0
			}
			if code != want {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
			}
			if mode == "corrupt object" && strings.Contains(out, "does not track") {
				t.Fatal(out)
			}
		})
	}
}

func TestImplementCheckRealGitHardening(t *testing.T) {
	f, repo := implementCheckGitFixture(t, true)
	marker := filepath.Join(f.root, "executed")
	hook := filepath.Join(f.root, "hooks/pre-commit")
	implementCheckWrite(t, hook, "#!/bin/sh\nprintf ran > '"+marker+"'\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	monitor := filepath.Join(f.root, "monitor")
	implementCheckWrite(t, monitor, "#!/bin/sh\nprintf ran > '"+marker+"'\n")
	if err := os.Chmod(monitor, 0700); err != nil {
		t.Fatal(err)
	}
	implementCheckGitOK(t, repo, "config", "core.hooksPath", filepath.Dir(hook))
	implementCheckGitOK(t, repo, "config", "core.fsmonitor", monitor)
	// Caller environment must not redirect git or supply configuration.
	t.Setenv("GIT_DIR", filepath.Join(f.root, "not-a-repo"))
	t.Setenv("GIT_CONFIG_COUNT", "invalid")
	t.Setenv("PATH", "/nonexistent")
	implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "--no-gpg-sign", "-m", "empty")
	code, out, _ := f.run()
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hook/fsmonitor ran: %v", err)
	}
	old := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", "HEAD:AGENTS.md"))
	implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "changed")
	replacement := strings.TrimSpace(implementCheckGitOK(t, repo, "hash-object", "-w", filepath.Join(f.tree, "AGENTS.md")))
	implementCheckGitOK(t, repo, "replace", old, replacement)
	// With replace objects enabled this exact diff is fooled. The hardened
	// runner must observe the real baseline blob instead.
	binary := "/usr/bin/git"
	if _, err := os.Stat(binary); errors.Is(err, fs.ErrNotExist) {
		binary = "/bin/git"
	}
	_, status, err := implementProcess(binary, []string{"-C", f.tree, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "diff", "--quiet", "--no-ext-diff", "--no-textconv", "origin/main", "--", "AGENTS.md"}, []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, time.Second*10)
	if err != nil || status != 0 {
		t.Fatalf("replace setup failed: %d %v", status, err)
	}
	code, out, _ = f.run()
	if code != 1 || !strings.Contains(out, `"AGENTS.md" differs from origin/main`) {
		t.Fatalf("replacement hid change: %d %s", code, out)
	}
}

func TestImplementCheckCatFileAmbiguous(t *testing.T) {
	_, repo := implementCheckGitFixture(t, true)
	_, missing, err := implementGit(repo)("cat-file", "-e", "HEAD:missing")
	if err != nil || missing != 128 {
		t.Fatalf("missing: %d %v", missing, err)
	}
	// A missing blob can exit 1; a missing tree makes revision:path resolution
	// fail with the same 128 as a path that is absent from an intact tree.
	oid := strings.TrimSpace(implementCheckGitOK(t, repo, "rev-parse", "HEAD^{tree}"))
	implementCheckRemove(t, filepath.Join(repo, ".git/objects", oid[:2], oid[2:]))
	_, corrupt, err := implementGit(repo)("cat-file", "-e", "HEAD:AGENTS.md")
	if err != nil || corrupt != 128 {
		t.Fatalf("corrupt: %d %v", corrupt, err)
	}
	out, status, err := implementGit(repo)("ls-tree", "-z", "HEAD", "--", "AGENTS.md")
	if err != nil || status != 128 {
		t.Fatalf("corrupt tree: %q %d %v", out, status, err)
	}
}

func implementCheckGitFixture(t *testing.T, agents bool) (*implementCheckTestFixture, string) {
	t.Helper()
	f := implementCheckFixture(t)
	repo := filepath.Join(f.root, "repo")
	implementCheckMkdir(t, repo)
	implementCheckGitOK(t, repo, "init", "--initial-branch=main")
	implementCheckWrite(t, filepath.Join(repo, ".github/trusted"), "trusted")
	if agents {
		implementCheckWrite(t, filepath.Join(repo, "AGENTS.md"), "baseline")
	}
	implementCheckGitOK(t, repo, "add", ".")
	implementCheckGitOK(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "baseline")
	implementCheckGitOK(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	implementCheckRemove(t, f.tree)
	implementCheckGitOK(t, repo, "worktree", "add", "--detach", f.tree, "HEAD")
	data, err := implementRead(filepath.Join(f.tree, ".git"), 64<<10, false)
	if err != nil {
		t.Fatal(err)
	}
	implementCheckWrite(t, f.saved, string(data))
	f.deps.git = implementGit(f.tree)
	return f, repo
}
func implementCheckGitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, status, err := implementGit(dir)(args...)
	if err != nil || status != 0 {
		t.Fatalf("git %v: status %d, error %v, stdout %q", args, status, err, out)
	}
	return string(out)
}
