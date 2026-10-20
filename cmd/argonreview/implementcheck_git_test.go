package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A complete fake plumbing fixture keeps error tests independent of filesystem
// mismatches: removing an error check must make its test fail.
func implementCheckBaselineFake(t *testing.T, tracked bool) *implementCheckTestFixture {
	t.Helper()
	f := implementCheckFixture(t)
	if tracked {
		implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "baseline")
	}
	fallback := f.deps.git
	f.deps.git = func(args ...string) ([]byte, int, error) {
		if args[0] == "cat-file" {
			return []byte("blob\n"), 0, nil
		}
		if tracked && args[len(args)-1] == "AGENTS.md" {
			oid := implementBlobOID([]byte("baseline"), "sha1")
			if args[0] == "ls-tree" {
				return []byte("100644 blob " + oid + "\tAGENTS.md\x00"), 0, nil
			}
			if args[0] == "ls-files" && args[1] == "-s" {
				return []byte("100644 " + oid + " 0\tAGENTS.md\x00"), 0, nil
			}
			if args[0] == "ls-files" && args[1] == "-v" {
				return []byte("H AGENTS.md\x00"), 0, nil
			}
		}
		return fallback(args...)
	}
	return f
}

func implementCheckBaselineRun(f *implementCheckTestFixture) (int, string) {
	var out bytes.Buffer
	c := implementChecker{deps: f.deps, out: &out}
	c.baseline(f.tree)
	return c.problems, out.String()
}

func implementCheckGitStage(args []string) string {
	switch args[0] {
	case "ls-tree":
		if args[3] == "HEAD" {
			return "HEAD"
		}
		return "reference"
	case "ls-files":
		return args[1]
	default:
		return args[0]
	}
}

func TestImplementCheckGitFailures(t *testing.T) {
	for _, stage := range []string{"rev-parse", "merge-base", "reference", "HEAD", "cat-file", "-s", "-v"} {
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
			{name: "status 1", status: 1}, {name: "status 2", status: 2}, {name: "status 128", status: 128},
		} {
			t.Run(stage+"/"+outcome.name, func(t *testing.T) {
				f := implementCheckBaselineFake(t, true)
				original := f.deps.git
				probedAbsent := false
				f.deps.lstat = func(p string) (fs.FileInfo, error) {
					if p == filepath.Join(f.tree, "AGENTS.md") && stage == "reference" {
						probedAbsent = true
					}
					return os.Lstat(p)
				}
				hit, calls := false, 0
				f.deps.git = func(args ...string) ([]byte, int, error) {
					calls++
					if implementCheckGitStage(args) == stage && (stage == "rev-parse" || stage == "merge-base" || stage == "cat-file" || args[len(args)-1] == "AGENTS.md") {
						hit = true
						data := outcome.out
						if outcome.err != nil || outcome.status != 0 {
							data, _, _ = original(args...)
						}
						return data, outcome.status, outcome.err
					}
					return original(args...)
				}
				problems, out := implementCheckBaselineRun(f)
				if !hit || problems == 0 || probedAbsent {
					t.Fatalf("hit %t, problems %d: %s", hit, problems, out)
				}
				if stage == "rev-parse" && calls != 1 || stage == "merge-base" && calls != 2 {
					t.Fatalf("continued after failed prerequisite: %d", calls)
				}
				if stage == "merge-base" && outcome.name == "status 1" && out != "CHECK: no merge base with origin/main\n" {
					t.Fatal(out)
				}
				want := "cannot list baseline"
				switch stage {
				case "rev-parse":
					want = "object format failed"
				case "merge-base":
					want = "merge base"
					if outcome.name != "status 1" {
						want = "merge-base"
					}
				case "cat-file":
					want = "checking baseline object"
				case "-v":
					want = "baseline index"
				}
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q: %s", want, out)
				}
			})
		}
	}
}

func TestImplementCheckGitAbsenceAndPresence(t *testing.T) {
	for _, mode := range []string{"absent exists", "absent stat error", "tracked changed", "tracked untracked", "absent", "tracked"} {
		t.Run(mode, func(t *testing.T) {
			tracked := strings.HasPrefix(mode, "tracked")
			f := implementCheckBaselineFake(t, tracked)
			if mode == "absent exists" || mode == "tracked changed" {
				implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "changed")
			}
			if mode == "tracked untracked" {
				implementCheckWrite(t, filepath.Join(f.tree, ".github/extra"), "extra")
			}
			if mode == "absent stat error" {
				f.deps.lstat = func(p string) (fs.FileInfo, error) {
					if filepath.Base(p) == "AGENTS.md" {
						return nil, fs.ErrPermission
					}
					return os.Lstat(p)
				}
			}
			original := f.deps.git
			calls := 0
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls++
				base := args[len(args)-1]
				var want []string
				switch implementCheckGitStage(args) {
				case "rev-parse":
					want = []string{"rev-parse", "--show-object-format"}
				case "merge-base":
					want = []string{"merge-base", "HEAD", "refs/remotes/origin/main"}
				case "reference":
					want = []string{"ls-tree", "-r", "-z", strings.Repeat("a", 40), "--", base}
				case "HEAD":
					want = []string{"ls-tree", "-r", "-z", "HEAD", "--", base}
				case "-s", "-v":
					want = []string{"ls-files", args[1], "-z", "--", base}
				case "cat-file":
					want = []string{"cat-file", "-t", implementBlobOID([]byte("baseline"), "sha1")}
				default:
					t.Fatalf("unexpected git %v", args)
				}
				if !reflect.DeepEqual(args, want) {
					t.Errorf("args %v, want %v", args, want)
				}
				return original(args...)
			}
			problems, out := implementCheckBaselineRun(f)
			good := mode == "absent" || mode == "tracked"
			if (problems == 0) != good {
				t.Fatal(out)
			}
			want := 22
			if tracked {
				want++
			}
			if calls != want {
				t.Fatalf("calls %d want %d", calls, want)
			}
		})
	}
}

func TestImplementCheckBaselineGitOutput(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, tc := range []struct {
			name, command, output, want string
			status                      int
			err                         error
		}{
			{name: "HEAD changed", command: "HEAD", status: 1, want: "cannot list baseline"},
			{name: "HEAD output", command: "HEAD", output: "garbage", want: "cannot list baseline"},
			{name: "HEAD NUL output", command: "HEAD", output: "\x00", want: "cannot list baseline"},
			{name: "HEAD status 2", command: "HEAD", status: 2, want: "cannot list baseline"},
			{name: "HEAD error", command: "HEAD", err: fs.ErrNotExist, want: "cannot list baseline"},
			{name: "HEAD signal", command: "HEAD", status: -1, err: errors.New("signal: killed"), want: "cannot list baseline"},
			{name: "cached changed", command: "-s", status: 1, want: "cannot list baseline"},
			{name: "cached output", command: "-s", output: "garbage", want: "cannot list baseline"},
			{name: "cached NUL output", command: "-s", output: "\x00", want: "cannot list baseline"},
			{name: "cached status 2", command: "-s", status: 2, want: "cannot list baseline"},
			{name: "cached error", command: "-s", err: fs.ErrNotExist, want: "cannot list baseline"},
			{name: "cached signal", command: "-s", status: -1, err: errors.New("signal: killed"), want: "cannot list baseline"},
			{name: "index status 2", command: "-v", status: 2, want: "listing baseline index"},
			{name: "index error", command: "-v", err: fs.ErrNotExist, want: "listing baseline index"},
			{name: "index signal", command: "-v", status: -1, err: errors.New("signal: killed"), want: "listing baseline index"},
			{name: "index short", command: "-v", output: "H\x00", want: "malformed baseline index record"},
			{name: "index empty", command: "-v", output: "\x00", want: "malformed baseline index record"},
			{name: "index no space", command: "-v", output: "HxAGENTS.md\x00", want: "malformed baseline index record"},
			{name: "index no path", command: "-v", output: "H \x00", want: "malformed baseline index record"},
			{name: "index unterminated", command: "-v", output: "H AGENTS.md", want: "malformed baseline index record"},
			{name: "index flags", command: "-v", output: "H AGENTS.md\x00h AGENTS.md\x00S AGENTS.md\x00s AGENTS.md\x00M AGENTS.md\x00R AGENTS.md\x00C AGENTS.md\x00K AGENTS.md\x00", want: "has tag"},
			{name: "index normal", command: "-v", output: "H AGENTS.md\x00"},
		} {
			t.Run(fmt.Sprintf("tracked=%t/%s", tracked, tc.name), func(t *testing.T) {
				f := implementCheckBaselineFake(t, tracked)
				original := f.deps.git
				f.deps.git = func(args ...string) ([]byte, int, error) {
					if implementCheckGitStage(args) == tc.command && args[len(args)-1] == "AGENTS.md" {
						return []byte(tc.output), tc.status, tc.err
					}
					return original(args...)
				}
				if tc.name == "index normal" && !tracked {
					wrapped := f.deps.git
					f.deps.git = func(args ...string) ([]byte, int, error) {
						if args[0] == "ls-files" && args[1] == "-s" && args[len(args)-1] == "AGENTS.md" {
							return []byte("100644 " + implementBlobOID([]byte("baseline"), "sha1") + " 0\tAGENTS.md\x00"), 0, nil
						}
						return wrapped(args...)
					}
					tc.want = "in the index but origin/main does not track it"
				}
				problems, out := implementCheckBaselineRun(f)
				if tc.want == "" {
					if problems != 0 {
						t.Fatal(out)
					}
				} else if problems == 0 || !strings.Contains(out, tc.want) {
					t.Fatalf("want %q: %s", tc.want, out)
				}
				if tc.name == "index flags" {
					for _, tag := range []string{"h", "S", "s", "M", "R", "C", "K"} {
						if !strings.Contains(out, `has tag "`+tag+`"`) {
							t.Fatal(out)
						}
					}
				}
			})
		}
	}
	// NUL framing must preserve spaces and newlines in actual baseline paths.
	f := implementCheckFixture(t)
	rel := ".github/path with\nspace"
	implementCheckWrite(t, filepath.Join(f.tree, rel), "baseline")
	oid := implementBlobOID([]byte("baseline"), "sha1")
	original := f.deps.git
	f.deps.git = func(args ...string) ([]byte, int, error) {
		if args[0] == "cat-file" {
			return []byte("blob\n"), 0, nil
		}
		if args[len(args)-1] == ".github" {
			if args[0] == "ls-tree" {
				return []byte("100644 blob " + oid + "\t" + rel + "\x00"), 0, nil
			}
			if args[1] == "-s" {
				return []byte("100644 " + oid + " 0\t" + rel + "\x00"), 0, nil
			}
			return []byte("H " + rel + "\x00"), 0, nil
		}
		return original(args...)
	}
	if problems, out := implementCheckBaselineRun(f); problems != 0 {
		t.Fatal(out)
	}
}

func TestImplementCheckTreeEntry(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, index := range []bool{false, true} {
		kind := "blob "
		if index {
			kind = ""
		}
		valid := "100644 " + kind + oid
		if index {
			valid += " 0"
		}
		valid += "\t.github/file\x00"
		cases := []struct{ name, data string }{
			{"wrong path", strings.ReplaceAll(valid, ".github/file", "wrong")},
			{"short oid", strings.ReplaceAll(valid, oid, "short")},
			{"long oid", strings.ReplaceAll(valid, oid, strings.Repeat("a", 64))},
			{"nonhex oid", strings.ReplaceAll(valid, oid, strings.Repeat("z", 40))},
			{"truncated", strings.TrimSuffix(valid, "\x00")},
			{"double space", strings.Replace(valid, " ", "  ", 1)},
			{"garbage", "garbage"}, {"extra NUL", valid + "\x00"},
			{"symlink", strings.Replace(valid, "100644", "120000", 1)},
			{"gitlink", strings.Replace(valid, "100644", "160000", 1)},
			{"tree mode", strings.Replace(valid, "100644", "040000", 1)},
			{"duplicate", valid + valid},
			{"dotdot", strings.ReplaceAll(valid, ".github/file", ".github/../file")},
			{"nested git", strings.ReplaceAll(valid, ".github/file", ".github/.GIT/file")},
			{"file parent", valid + strings.ReplaceAll(valid, ".github/file", ".github/file/child")},
		}
		if index {
			for _, stage := range []string{"1", "2", "3", "x"} {
				cases = append(cases, struct{ name, data string }{"stage " + stage, strings.Replace(valid, " 0\t", " "+stage+"\t", 1)})
			}
		} else {
			for _, kind := range []string{"tree", "commit", "unknown"} {
				cases = append(cases, struct{ name, data string }{"type " + kind, strings.Replace(valid, "blob", kind, 1)})
			}
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("index=%t/%s", index, tc.name), func(t *testing.T) {
				if _, ok := implementTreeEntry([]byte(tc.data), ".github", 40, index); ok {
					t.Fatalf("accepted %q", tc.data)
				}
				f := implementCheckFixture(t)
				original := f.deps.git
				f.deps.git = func(args ...string) ([]byte, int, error) {
					target := args[len(args)-1] == ".github"
					if target && (index && args[0] == "ls-files" && args[1] == "-s" || !index && args[0] == "ls-tree" && args[3] != "HEAD") {
						return []byte(tc.data), 0, nil
					}
					return original(args...)
				}
				problems, out := implementCheckBaselineRun(f)
				if problems == 0 || !strings.Contains(out, "cannot list baseline") {
					t.Fatal(out)
				}
			})
		}
		for _, mode := range []string{"100644", "100755"} {
			if _, ok := implementTreeEntry([]byte(strings.Replace(valid, "100644", mode, 1)), ".github", 40, index); !ok {
				t.Fatalf("refused %s", mode)
			}
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
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_NO_LAZY_FETCH", "GIT_NO_REPLACE_OBJECTS", "GIT_OPTIONAL_LOCKS", "GIT_ASKPASS", "GIT_TERMINAL_PROMPT", "GIT_PROTOCOL_FROM_USER", "GIT_ALLOW_PROTOCOL", "PATH", "HOME"} {
		t.Setenv(name, "hostile")
	}
	want := []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "GIT_PROTOCOL_FROM_USER=0", "GIT_ALLOW_PROTOCOL="}
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
		if entry != "GIT_NO_LAZY_FETCH=1" && entry != "GIT_PROTOCOL_FROM_USER=0" && entry != "GIT_ALLOW_PROTOCOL=" {
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

func TestImplementCheckGitFileGate(t *testing.T) {
	for _, mode := range []string{"mismatch", "actual missing", "saved missing", "actual directory", "saved directory", "equal"} {
		t.Run(mode, func(t *testing.T) {
			f := implementCheckFixture(t)
			path := filepath.Join(f.tree, ".git")
			if strings.HasPrefix(mode, "saved") {
				path = f.saved
			}
			switch {
			case mode == "mismatch":
				implementCheckWrite(t, path, "changed")
			case strings.HasSuffix(mode, "missing"):
				implementCheckRemove(t, path)
			case strings.HasSuffix(mode, "directory"):
				implementCheckRemove(t, path)
				implementCheckMkdir(t, path)
			}
			calls := 0
			git := f.deps.git
			f.deps.git = func(args ...string) ([]byte, int, error) {
				calls++
				return git(args...)
			}
			code, out, stderr := f.run()
			if mode == "equal" {
				if code != 0 || calls == 0 || out != "implement-check: ok\n" || stderr != "" {
					t.Fatalf("git calls %d, exit %d: %s %s", calls, code, out, stderr)
				}
			} else if code != 1 || calls != 0 || !strings.Contains(out, ".git file changed") {
				t.Fatalf("git calls %d, exit %d: %s %s", calls, code, out, stderr)
			}
		})
	}
}

func TestImplementCheckRealGitReplacedGit(t *testing.T) {
	for _, mode := range []string{"directory", "gitdir pointer"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			path := filepath.Join(f.tree, ".git")
			implementCheckRemove(t, path)
			implementCheckGitOK(t, f.tree, "init", "--initial-branch=main")
			implementCheckGitOK(t, f.tree, "add", "--", "AGENTS.md")
			implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "attacker baseline")
			implementCheckGitOK(t, f.tree, "update-ref", "refs/remotes/origin/main", "HEAD")
			gitdir := path
			if mode == "gitdir pointer" {
				gitdir = filepath.Join(f.root, "evil-git")
				if err := os.Rename(path, gitdir); err != nil {
					t.Fatal(err)
				}
				implementCheckWrite(t, path, "gitdir: "+gitdir+"\n")
			}
			marker := filepath.Join(f.root, "filter-executed")
			script := filepath.Join(f.root, "evil-clean")
			implementCheckWrite(t, script, "#!/bin/sh\nprintf ran > '"+marker+"'\n/bin/cat\n")
			if err := os.Chmod(script, 0700); err != nil {
				t.Fatal(err)
			}
			implementCheckWrite(t, filepath.Join(gitdir, "info/attributes"), "AGENTS.md filter=evil\n")
			implementCheckGitOK(t, f.tree, "config", "filter.evil.clean", "'"+script+"'")
			implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "attacker change\n")
			code, out, stderr := f.run()
			if code != 1 || !strings.Contains(out, ".git file changed") {
				t.Errorf("exit %d: %s %s", code, out, stderr)
			}
			if _, err := os.Lstat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("clean filter ran outside the worktree: marker stat %v", err)
			}
		})
	}
}

func TestImplementCheckRealGitShadowedRemote(t *testing.T) {
	for _, mode := range []string{"tag", "branch", "unshadowed"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			implementCheckWrite(t, filepath.Join(f.tree, "AGENTS.md"), "committed change")
			implementCheckGitOK(t, f.tree, "add", "--", "AGENTS.md")
			implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "changed baseline")
			if mode != "unshadowed" {
				ref := "refs/tags/origin/main"
				if mode == "branch" {
					ref = "refs/heads/origin/main"
				}
				implementCheckGitOK(t, f.tree, "update-ref", ref, "HEAD")
			}
			code, out, stderr := f.run()
			if code != 1 || !strings.Contains(out, `"AGENTS.md" differs from origin/main in HEAD`) {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
			}
		})
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

func TestImplementCheckRealGitHiddenBaseline(t *testing.T) {
	for _, mode := range []string{"committed restored", "committed removed", "staged deleted", "assume unchanged", "skip worktree"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			base, want := "AGENTS.md", `"AGENTS.md" differs from origin/main in HEAD`
			if mode == "committed removed" || mode == "staged deleted" {
				base = "CLAUDE.md"
				want = `"CLAUDE.md" differs from origin/main in HEAD`
			}
			switch mode {
			case "committed restored", "committed removed", "staged deleted":
				implementCheckWrite(t, filepath.Join(f.tree, base), "changed")
				implementCheckGitOK(t, f.tree, "add", "--", base)
				if mode == "staged deleted" {
					implementCheckRemove(t, filepath.Join(f.tree, base))
					want = `"CLAUDE.md" is in the index but origin/main does not track it`
				} else {
					implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "changed baseline")
					if mode == "committed restored" {
						implementCheckGitOK(t, f.tree, "restore", "--source=origin/main", "--staged", "--worktree", "--", base)
					} else {
						implementCheckGitOK(t, f.tree, "rm", "--", base)
					}
				}
			case "assume unchanged", "skip worktree":
				flag, tag := "--assume-unchanged", "h"
				if mode == "skip worktree" {
					flag, tag = "--skip-worktree", "S"
				}
				implementCheckGitOK(t, f.tree, "update-index", flag, "--", base)
				implementCheckWrite(t, filepath.Join(f.tree, base), "changed")
				want = `baseline index "AGENTS.md" has tag "` + tag + `"`
			}
			// These attacks all pass the original worktree-only comparison.
			if mode != "staged deleted" {
				implementCheckGitOK(t, f.tree, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "origin/main", "--", base)
			}
			code, out, stderr := f.run()
			if code != 1 || !strings.Contains(out, want) {
				t.Fatalf("want %q, exit %d: %s %s", want, code, out, stderr)
			}
		})
	}
}

func TestImplementCheckRealGitIndexBaseline(t *testing.T) {
	for _, mode := range []string{"staged addition deleted", "staged modification restored", "staged deletion restored", "clean", "clean absent AGENTS"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, mode != "clean absent AGENTS")
			base := "AGENTS.md"
			switch mode {
			case "staged addition deleted":
				base = ".github"
				path := filepath.Join(f.tree, base, "workflows/evil.yml")
				implementCheckWrite(t, path, "changed")
				implementCheckGitOK(t, f.tree, "add", "--", base)
				implementCheckRemove(t, path)
			case "staged modification restored":
				implementCheckWrite(t, filepath.Join(f.tree, base), "changed")
				implementCheckGitOK(t, f.tree, "add", "--", base)
				implementCheckWrite(t, filepath.Join(f.tree, base), "baseline")
			case "staged deletion restored":
				implementCheckGitOK(t, f.tree, "rm", "--", base)
				implementCheckWrite(t, filepath.Join(f.tree, base), "baseline")
			}
			code, out, stderr := f.run()
			if strings.HasPrefix(mode, "clean") {
				if code != 0 || out != "implement-check: ok\n" || stderr != "" {
					t.Fatalf("exit %d: %s %s", code, out, stderr)
				}
			} else if want := fmt.Sprintf("%q index differs from the merge base", base); code != 1 || !strings.Contains(out, want) {
				t.Fatalf("want %q, exit %d: %s %s", want, code, out, stderr)
			}
		})
	}
}

func TestImplementCheckGithubGitNames(t *testing.T) {
	for _, rel := range []string{".github/.git/inside", ".github/sub/.git", ".github/.GIT", ".github/workflows/x.yml"} {
		t.Run(rel, func(t *testing.T) {
			f, _ := implementCheckGitFixture(t, true)
			implementCheckWrite(t, filepath.Join(f.tree, rel), "file")
			if rel == ".github/workflows/x.yml" {
				implementCheckGitOK(t, f.tree, "add", "--", rel)
				implementCheckGitOK(t, f.tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "trusted workflow")
				implementCheckGitOK(t, f.tree, "update-ref", "refs/remotes/origin/main", "HEAD")
			}
			code, out, stderr := f.run()
			if rel == ".github/workflows/x.yml" {
				if code != 0 || out != "implement-check: ok\n" {
					t.Fatalf("exit %d: %s %s", code, out, stderr)
				}
				return
			}
			name := strings.TrimSuffix(rel, "/inside")
			if code != 1 || !strings.Contains(out, "refused name in worktree: "+fmt.Sprintf("%q", filepath.Join(f.tree, name))) {
				t.Fatalf("exit %d: %s %s", code, out, stderr)
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
