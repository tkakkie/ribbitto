package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		allow       bool
		reason      string
	}{
		{"view_file", `{"toolCall":{"name":"view_file","args":{"AbsolutePath":"/w/main.go"}}}`, true, ""},
		{"list_dir", `{"toolCall":{"name":"list_dir"}}`, true, ""},
		{"grep_search", `{"toolCall":{"name":"grep_search"}}`, true, ""},
		{"find_by_name", `{"toolCall":{"name":"find_by_name"}}`, true, ""},
		{"known tool", `{"toolCall":{"name":"run_command","args":{"CommandLine":"ls"}}}`, false, ""},
		{"unblockable by deny rules", `{"toolCall":{"name":"search_web"}}`, false, ""},
		{"unknown tool", `{"toolCall":{"name":"view_file_v2"}}`, false, ""},
		{"malformed", `{"toolCall":`, false, ""},
		{"empty", ``, false, ""},
		{"no tool call", `{"stepIdx":3}`, false, "names no tool"},
		{"nameless", `{"toolCall":{"args":{}}}`, false, "names no tool"},
		{"name of another type", `{"toolCall":{"name":7}}`, false, ""},
		{"oversized", `{"toolCall":{"name":"view_file"},"pad":"` + strings.Repeat("x", maxGuardInput) + `"}`, false, "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := run([]string{"guard"}, strings.NewReader(tc.input), &out, &bytes.Buffer{}); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var decision map[string]string
			if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
				t.Fatalf("output %q: %v", out.String(), err)
			}
			want := "deny"
			if tc.allow {
				want = "allow"
			}
			if decision["decision"] != want || !tc.allow && decision["reason"] == "" {
				t.Fatalf("decision = %v, want %s with a reason for a deny", decision, want)
			}
			if !strings.Contains(decision["reason"], tc.reason) {
				t.Fatalf("reason = %q, want %q", decision["reason"], tc.reason)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

// A stdin that fails mid-read is a deny, never a missing answer.
func TestGuardReadError(t *testing.T) {
	var out bytes.Buffer
	guard(failingReader{}, &out)
	if strings.TrimSpace(out.String()) != `{"decision":"deny","reason":"unreadable hook input"}` {
		t.Fatalf("output %q", out.String())
	}
}

func TestTrusted(t *testing.T) {
	const head = "4b4f99a52fd3c87e5bb049735b2a04b3fa8ff482"
	build := func(settings ...string) *debug.BuildInfo {
		info := &debug.BuildInfo{}
		for i := 0; i < len(settings); i += 2 {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
		}
		return info
	}
	resolved := func() (string, error) { return head, nil }
	for _, tc := range []struct {
		name    string
		info    *debug.BuildInfo
		ok      bool
		resolve func() (string, error)
		want    string
	}{
		{"clean origin/main", build("vcs.revision", head, "vcs.modified", "false"), true, resolved, ""},
		{"another revision", build("vcs.revision", "0242656c39ecae5d3b28048bbc7f612018d5d9a3", "vcs.modified", "false"), true, resolved, "but origin/main is"},
		{"dirty", build("vcs.revision", head, "vcs.modified", "true"), true, resolved, "modified or unstamped"},
		{"no modified flag", build("vcs.revision", head), true, resolved, "modified or unstamped"},
		{"unstamped", build(), true, resolved, "no VCS revision"},
		{"no build information", nil, false, resolved, "no build information"},
		{"unresolvable origin/main", build("vcs.revision", head, "vcs.modified", "false"), true, func() (string, error) { return "", errors.New("exit status 1") }, "resolving origin/main"},
		{"empty origin/main", build("vcs.revision", head, "vcs.modified", "false"), true, func() (string, error) { return "", nil }, "but origin/main is"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := trusted(tc.info, tc.ok, tc.resolve)
			if tc.want == "" {
				if err != nil || got != head {
					t.Fatalf("trusted = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSourceRevision(t *testing.T) {
	const head = "4b4f99a52fd3c87e5bb049735b2a04b3fa8ff482"
	for _, tc := range []struct {
		name, source, origin string
		fail                 bool
		want                 string
	}{
		{"ribbitto over https", "/src", "https://github.com/tkakkie/ribbitto.git", false, ""},
		{"ribbitto over ssh", "/src", "git@github.com:tkakkie/ribbitto.git", false, ""},
		{"unset", "", "https://github.com/tkakkie/ribbitto.git", false, "RIBBITTO_ARGON_SOURCE"},
		{"relative", "src", "https://github.com/tkakkie/ribbitto.git", false, "RIBBITTO_ARGON_SOURCE"},
		{"another repository", "/src", "https://github.com/someone/ribbitto.git", false, "not github.com/tkakkie/ribbitto"},
		{"a lookalike", "/src", "https://github.com/tkakkie/ribbitto-fork", false, "not github.com/tkakkie/ribbitto"},
		{"no origin", "/src", "", true, "origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every git call must name the source checkout, never the current
			// directory or a review target.
			git := func(dir string, args ...string) (string, error) {
				if dir != tc.source {
					t.Fatalf("git ran in %q, not the source %q", dir, tc.source)
				}
				if args[0] == "remote" {
					if tc.fail {
						return "", errors.New("exit status 2")
					}
					return tc.origin, nil
				}
				return head, nil
			}
			got, err := sourceRevision(tc.source, git)
			if tc.want == "" {
				if err != nil || got != head {
					t.Fatalf("sourceRevision = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// With real git, origin/main comes from the source checkout even when the
// current directory is another repository with its own origin/main.
func TestSourceRevisionIgnoresTheCurrentDirectory(t *testing.T) {
	repo := func(origin string) (string, string) {
		dir := t.TempDir()
		for _, args := range [][]string{
			{"init", "-q"},
			{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", dir},
			{"remote", "add", "origin", origin},
		} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		head, err := git(dir, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := git(dir, "update-ref", "refs/remotes/origin/main", head); err != nil {
			t.Fatal(err)
		}
		return dir, head
	}
	source, want := repo("https://github.com/tkakkie/ribbitto.git")
	target, other := repo("https://github.com/tkakkie/ribbitto.git")
	t.Chdir(target)
	got, err := sourceRevision(source, git)
	if err != nil || got != want || got == other {
		t.Fatalf("sourceRevision = %q, %v; source %s, current directory %s", got, err, want, other)
	}
	// An inherited GIT_DIR or GIT_WORK_TREE overrides -C unless removed.
	t.Setenv("GIT_DIR", filepath.Join(target, ".git"))
	t.Setenv("GIT_WORK_TREE", target)
	got, err = sourceRevision(source, git)
	if err != nil || got != want {
		t.Fatalf("with GIT_DIR on the target: sourceRevision = %q, %v; want the source's %s, not %s", got, err, want, other)
	}
}

// A url.*.insteadOf in the global configuration, found through HOME or
// XDG_CONFIG_HOME, must not make a fork pass as ribbitto.
func TestSourceRevisionIgnoresGlobalConfiguration(t *testing.T) {
	const fork, ribbitto = "https://github.com/someone/fork", "https://github.com/tkakkie/ribbitto"
	rewrite := "[url \"" + ribbitto + "\"]\n\tinsteadOf = " + fork + "\n"
	for _, where := range []string{"HOME", "XDG_CONFIG_HOME"} {
		t.Run(where, func(t *testing.T) {
			source := t.TempDir()
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", fork}} {
				if out, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			home, xdg := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".gitconfig")
			if where == "XDG_CONFIG_HOME" {
				path = filepath.Join(xdg, "git", "config")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(rewrite), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			// The rewrite is live for a plain git call, so the test can fail.
			out, err := exec.Command("git", "-C", source, "remote", "get-url", "origin").Output()
			if err != nil || strings.TrimSpace(string(out)) != ribbitto {
				t.Fatalf("plain get-url = %q, %v; the fixture's rewrite is not in effect", out, err)
			}
			if _, err := sourceRevision(source, git); err == nil || !strings.Contains(err.Error(), "not github.com/tkakkie/ribbitto") {
				t.Fatalf("err = %v, want the fork refused", err)
			}
		})
	}
}

// A test binary carries no VCS stamp, so verify must refuse it.
func TestVerifyRefusesThisBinary(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"verify"}, nil, &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "argonreview: ") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"review"}, {"guard", "extra"}} {
		var stderr bytes.Buffer
		if code := run(args, nil, &bytes.Buffer{}, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr.String())
		}
	}
}
