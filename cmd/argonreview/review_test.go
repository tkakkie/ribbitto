package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const okResult = `{"event":"result","result":{"status":"SUCCESS","response":"No findings.\n"}}` + "\n"

// fakeAgy writes an agy into dir that records how it ran in dir/seen, exits
// 9 unless prepare has run and 8 unless it runs in the sanitised checkout of
// pull request 7, leaves a file in its TMPDIR as the real one does, prints
// dir/stdout and dir/stderr, appends to the
// file named in dir/touch, runs dir/mutate in its directory, sleeps if
// dir/sleep exists, and exits with dir/status.
func fakeAgy(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
{ echo "HOME=$HOME"; echo "TMPDIR=$TMPDIR"; echo "PWD=$(pwd -P)"; for a in "$@"; do echo "ARG=$a"; done; } > '` + dir + `/seen'
[ -f "$HOME/prepared" ] || exit 9
[ "$(cat README)" = two ] && [ -z "$(find . -iname .agents)" ] || exit 8
[ -n "$TMPDIR" ] && echo '{}' > "$TMPDIR/agy-cache.json"
cat '` + dir + `/stdout'
cat '` + dir + `/stderr' >&2
if [ -f '` + dir + `/touch' ]; then echo changed >> "$(cat '` + dir + `/touch')"; fi
if [ -f '` + dir + `/mutate' ]; then sh '` + dir + `/mutate'; fi
if [ -f '` + dir + `/sleep' ]; then exec sleep 30; fi
exit "$(cat '` + dir + `/status')"
`
	write(t, filepath.Join(dir, "agy"), script)
	if err := os.Chmod(filepath.Join(dir, "agy"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "stdout"), okResult)
	write(t, filepath.Join(dir, "stderr"), "")
	write(t, filepath.Join(dir, "status"), "0")
	return filepath.Join(dir, "agy")
}

// testReview is a review with the fixture's source checkout and pull request.
type testReview struct {
	review
	source string
	pr     target
}

// newReview returns a review of pull request 7 of pullRequests' fixture, run
// by fakeAgy, with the fake's directory. The dedicated HOME, the fake and the
// account's shared settings live outside the fixture's TMPDIR.
func newReview(t *testing.T) (testReview, string) {
	t.Helper()
	fake, home, account := t.TempDir(), resolved(t, t.TempDir()), t.TempDir()
	shared := filepath.Join(account, sharedSettings)
	write(t, shared, "{}\n")
	agy := fakeAgy(t, fake)
	source, revision, pr := pullRequests(t)
	return testReview{review{
		revision: revision, home: home, shared: shared, agy: agy,
		prepare: func() error { write(t, filepath.Join(home, "prepared"), ""); return nil },
		build:   func() (*workspace, error) { return newWorkspace(source, revision, originOf(source), pr, rawGit) },
		sup:     supervisor{10 * time.Second, time.Second, nil, systemCleanup()},
	}, source, pr}, fake
}

// afterCleanup is a buffer that checks nothing is left before each write.
type afterCleanup struct {
	bytes.Buffer
	t      *testing.T
	source string
}

func (b *afterCleanup) Write(p []byte) (int, error) {
	nothingLeft(b.t, b.source)
	return b.Buffer.Write(p)
}

func TestReview(t *testing.T) {
	r, fake := newReview(t)
	out := afterCleanup{t: t, source: r.source}
	if code, err := r.run(&out); code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	pr := r.pr
	if want := "Argon review of tkakkie/ribbitto#7 at " + pr.Head + "\nLauncher built from " + r.revision + "\n\nNo findings.\n"; out.String() != want {
		t.Fatalf("report:\n%s\nwant:\n%s", out.String(), want)
	}
	data, err := os.ReadFile(filepath.Join(fake, "seen"))
	if err != nil {
		t.Fatal(err)
	}
	seen := string(data)
	for _, want := range []string{
		"HOME=" + r.home + "\n", "TMPDIR=" + os.Getenv("TMPDIR") + "/argonreview-", "/tmp\nPWD=",
		"PWD=" + os.Getenv("TMPDIR") + "/argonreview-", "/tree\nARG=-p\n",
		"ARG=-p\nARG=Review adversarially.\n", "UNTRUSTED_PAYLOAD_JSON: {",
		"ARG=--model\nARG=gemini-4-argon-high\nARG=--sandbox\nARG=--print-timeout\nARG=5s\nARG=--output-format\nARG=stream-json\n",
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("agy ran without %q:\n%s", want, seen)
		}
	}
	nothingLeft(t, r.source)
}

func TestReviewFailureRules(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, status string
		sleep                        bool
		want                         string
		code                         int
	}{
		{"non-zero exit", okResult, "", "3", false, "status 3", 3},
		{"time limit", okResult, "", "0", true, "status 124", 124},
		{"denied_actions", `{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":["read_file"]}}` + "\n", "", "0", false, "denied_actions", 1},
		{"denied_actions with a response", `{"event":"result","result":{"status":"SUCCESS","response":"x","denied_actions":["read_file"]}}` + "\n", "", "0", false, "denied_actions", 1},
		{"empty response", `{"event":"result","result":{"status":"SUCCESS","response":" \n"}}` + "\n", "", "0", false, "empty", 1},
		{"AGY_ERROR", okResult, "AGY_ERROR: quota\n", "0", false, "AGY_ERROR", 1},
		{"a line that is not JSON", "not json\n" + okResult, "", "0", false, "line 1", 1},
		{"an event without a name", `{"step":1}` + "\n" + okResult, "", "0", false, "line 1", 1},
		{"no result event", `{"event":"init"}` + "\n", "", "0", false, "no result event", 1},
		{"output cut off", strings.TrimSuffix(okResult, "\n"), "", "0", false, "cut off", 1},
		{"no output", "", "", "0", false, "cut off", 1},
		{"a result that cannot be parsed", `{"event":"result","result":"oops"}` + "\n", "", "0", false, "cannot be parsed", 1},
		{"no response field", `{"event":"result","result":{"status":"SUCCESS"}}` + "\n", "", "0", false, "no response", 1},
		{"not a success", `{"event":"result","result":{"status":"ERROR","response":"x"}}` + "\n", "", "0", false, `status is "ERROR"`, 1},
		{"an event after the result", okResult + `{"event":"init"}` + "\n", "", "0", false, "not the last", 1},
		// A later duplicate would replace the evidence (Codex on #1113).
		{"denied_actions overwritten", `{"event":"result","result":{"status":"SUCCESS","response":"x","denied_actions":["read_file"],"denied_actions":null}}` + "\n", "", "0", false, "duplicate key", 1},
		{"denied_actions overwritten in another case", `{"event":"result","result":{"status":"SUCCESS","response":"x","denied_actions":["read_file"],"DENIED_ACTIONS":null}}` + "\n", "", "0", false, "duplicate key", 1},
		{"status overwritten", `{"event":"result","result":{"status":"ERROR","response":"x","status":"SUCCESS"}}` + "\n", "", "0", false, "duplicate key", 1},
		{"result overwritten", `{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":["x"]},"result":{"status":"SUCCESS","response":"x"}}` + "\n", "", "0", false, "duplicate key", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, fake := newReview(t)
			write(t, filepath.Join(fake, "stdout"), tc.stdout)
			write(t, filepath.Join(fake, "stderr"), tc.stderr)
			write(t, filepath.Join(fake, "status"), tc.status)
			if tc.sleep {
				write(t, filepath.Join(fake, "sleep"), "")
				r.sup.limit = time.Second
			}
			var out bytes.Buffer
			if code, err := r.run(&out); code != tc.code || err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 {
				t.Fatalf("code = %d, err = %v, output %q; want %d and %q", code, err, out.String(), tc.code, tc.want)
			}
			nothingLeft(t, r.source)
		})
	}
}

// A run that changes the sanitised tree does not count: tracked, untracked
// and ignored files alike, and a recreated .agents (#1071).
// Changes undone before agy exits count too, and a huge file left behind
// fails promptly without being read (Codex on #1117).
func TestReviewRefusesAChangedTree(t *testing.T) {
	for name, script := range map[string]string{
		"tracked":              "echo more >> README",
		"untracked":            "touch new.txt",
		"ignored":              "touch debug.log",
		"removed":              "rm a/keep.txt",
		".agents":              "mkdir -p a/.agents",
		"rewritten, restored":  `c=$(cat README); echo other > README; printf '%s\n' "$c" > README`,
		".agents, then gone":   "mkdir a/.agents && rmdir a/.agents",
		"huge sparse, ignored": "dd if=/dev/zero of=huge.log bs=1 count=0 seek=100G 2>/dev/null",
	} {
		t.Run(name, func(t *testing.T) {
			r, fake := newReview(t)
			write(t, filepath.Join(fake, "mutate"), script+"\n")
			var out bytes.Buffer
			started := time.Now()
			if code, err := r.run(&out); code != 1 || err == nil || !strings.Contains(err.Error(), "changed the worktree") || out.Len() != 0 || time.Since(started) > 10*time.Second {
				t.Fatalf("code = %d, err = %v, output %q after %v", code, err, out.String(), time.Since(started))
			}
			nothingLeft(t, r.source)
		})
	}
}

// A change that leaves the metadata as recorded, as a coarse timestamp can,
// is still found: content by its hash, an addition by its unknown name, a
// removal by the count (Codex on #1117).
func TestVerifyBeyondMetadata(t *testing.T) {
	for name, change := range map[string]func(path string) string{
		"content":  func(path string) string { write(t, path, "two\n"); return path },
		"addition": func(path string) string { write(t, path+"2", ""); return path + "2" },
		"removal": func(path string) string {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			return path
		},
	} {
		t.Run(name, func(t *testing.T) {
			tree := t.TempDir()
			path := filepath.Join(tree, "a/file")
			write(t, path, "one\n")
			recorded, err := record(tree)
			if err != nil {
				t.Fatal(err)
			}
			want := change(path)
			// Whatever now exists keeps its recorded metadata.
			for p := range recorded {
				if meta, _, _, err := metadata(p); err == nil {
					e := recorded[p]
					e.meta = meta
					recorded[p] = e
				}
			}
			if changed, err := verify(tree, recorded); changed != want || err != nil {
				t.Fatalf("verify = %q, %v; want %s", changed, err, want)
			}
		})
	}
}

// sameMetadata sets every recorded entry's metadata to what is there now,
// as a coarse timestamp could leave it.
func sameMetadata(recorded map[string]entry) {
	for p := range recorded {
		if meta, _, _, err := metadata(p); err == nil {
			e := recorded[p]
			e.meta = meta
			recorded[p] = e
		}
	}
}

// Verification reads a file only up to its recorded size: a recorded 4-byte
// file now 100 GB is found at once, not read through (Codex on #1117).
func TestVerifyReadsWithinTheRecord(t *testing.T) {
	tree := t.TempDir()
	path := filepath.Join(tree, "file")
	write(t, path, "one\n")
	recorded, err := record(tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 100<<30); err != nil {
		t.Fatal(err)
	}
	sameMetadata(recorded)
	started := time.Now()
	if changed, err := verify(tree, recorded); changed != path || err != nil || time.Since(started) > 5*time.Second {
		t.Fatalf("verify = %q, %v after %v", changed, err, time.Since(started))
	}
}

// Verification lists a directory a chunk at a time and stops at the first
// unknown name, so thousands of new entries cost one chunk (Codex on #1117).
func TestVerifyListsWithinOneChunk(t *testing.T) {
	tree := t.TempDir()
	write(t, filepath.Join(tree, "d/known"), "")
	recorded, err := record(tree)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		write(t, filepath.Join(tree, "d", fmt.Sprintf("new%d", i)), "")
	}
	sameMetadata(recorded)
	read := 0
	counting := func(dir *os.File) ([]string, error) {
		names, err := listNames(dir)
		read += len(names)
		return names, err
	}
	if changed, err := verifyWith(tree, recorded, counting); !strings.HasPrefix(changed, filepath.Join(tree, "d")+"/") || err != nil || read > 256+1 {
		t.Fatalf("verify = %q, %v after reading %d names", changed, err, read)
	}
}

// Whatever a run leaves behind, many files and a huge one, the check fails
// promptly and the workspace is removed; a hang or an exhausted memory would
// miss the deadline or end the helper by a signal (Codex on #1117).
func TestReviewChecksALargeChangePromptly(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLargeChangeHelper$")
	cmd.Env = append(os.Environ(), "ARGONREVIEW_LARGE_HELPER="+dir, "TMPDIR="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil || cmd.ProcessState.ExitCode() != 1 || !strings.Contains(stderr.String(), "changed the worktree") {
		t.Fatalf("helper: %v, exit %d, deadline %v\n%s", err, cmd.ProcessState.ExitCode(), ctx.Err(), stderr.String())
	}
	workspaces, err := os.ReadFile(filepath.Join(dir, "tmpdir"))
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(string(workspaces)); err != nil || len(entries) != 0 {
		t.Fatalf("left behind: %v, %v", entries, err)
	}
}

func TestLargeChangeHelper(t *testing.T) {
	dir := os.Getenv("ARGONREVIEW_LARGE_HELPER")
	if dir == "" {
		t.Skip("run by TestReviewChecksALargeChangePromptly")
	}
	r, fake := newReview(t)
	write(t, filepath.Join(fake, "mutate"), "mkdir many && (cd many && seq 1 20000 | xargs touch) && dd if=/dev/zero of=a/huge.log bs=1 count=0 seek=100G 2>/dev/null\n")
	write(t, filepath.Join(dir, "tmpdir"), os.Getenv("TMPDIR"))
	_, err := r.run(&bytes.Buffer{})
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

// A probe runs the same pipeline on a throwaway repository's HEAD with a
// trusted prompt file, and prints agy's output and the verdict whether the
// run counts or not (#1071).
func TestProbe(t *testing.T) {
	repo, promptFile := t.TempDir(), filepath.Join(t.TempDir(), "probe.md")
	gitT(t, repo, "init", "--quiet")
	write(t, filepath.Join(repo, "README"), "two\n")
	write(t, filepath.Join(repo, ".agents/hooks.json"), "{}\n")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "--quiet", "-m", "probe target")
	head := gitT(t, repo, "rev-parse", "HEAD")
	write(t, promptFile, "Read /etc/passwd.\n")
	for _, tc := range []struct {
		name, stdout, verdict string
		code                  int
	}{
		{"denied", `{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":["read_file"]}}` + "\n", "Verdict: does not count: the run does not count: the result lists denied_actions", 1},
		{"counted", okResult, "Verdict: counts", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, fake := newReview(t)
			r.probe = true
			r.build = func() (*workspace, error) { return probeWorkspace(r.source, r.revision, repo, promptFile, rawGit) }
			write(t, filepath.Join(fake, "stdout"), tc.stdout)
			var out bytes.Buffer
			code, err := r.run(&out)
			if code != tc.code || (err == nil) != (tc.code == 0) {
				t.Fatalf("code = %d, err = %v", code, err)
			}
			want := "Argon probe of " + repo + " at " + head + "\nLauncher built from " + r.revision + "\n\n" + tc.verdict
			if !strings.HasPrefix(out.String(), want) || !strings.HasSuffix(out.String(), "--- agy stdout\n"+tc.stdout+"--- agy stderr\n") {
				t.Fatalf("output:\n%s\nwant prefix:\n%s", out.String(), want)
			}
			if seen, err := os.ReadFile(filepath.Join(fake, "seen")); err != nil || !strings.Contains(string(seen), "ARG=-p\nARG=Read /etc/passwd.\n\nARG=--model") {
				t.Fatalf("agy ran as:\n%s, %v", seen, err)
			}
			nothingLeft(t, r.source)
		})
	}
	// A symlink is refused before the launch, recorded with the target's
	// identity apart from any tool denial (Codex on #1117).
	symlink(t, "/etc/passwd", filepath.Join(repo, "link"))
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "--quiet", "-m", "link")
	head = gitT(t, repo, "rev-parse", "HEAD")
	r, fake := newReview(t)
	r.probe = true
	r.build = func() (*workspace, error) { return probeWorkspace(r.source, r.revision, repo, promptFile, rawGit) }
	var out bytes.Buffer
	want := "Argon probe of " + repo + " at " + head + "\nLauncher built from " + r.revision + "\n\nVerdict: refused before launch: "
	if code, err := r.run(&out); code != 1 || err == nil || !strings.HasPrefix(out.String(), want) || !strings.Contains(out.String(), "symlink: link") {
		t.Fatalf("code = %d, err = %v, output:\n%s", code, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(fake, "seen")); !os.IsNotExist(err) {
		t.Fatalf("agy ran on a tree with a symlink: %v", err)
	}
	nothingLeft(t, r.source)
}

// probe runs only after the trusted-build check, like review.
func TestProbeChecksTheBuildFirst(t *testing.T) {
	bin, home := t.TempDir(), resolved(t, t.TempDir())
	write(t, filepath.Join(bin, "agy"), "#!/bin/sh\ntouch \""+bin+"/ran\"\n")
	if err := os.Chmod(filepath.Join(bin, "agy"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RIBBITTO_ARGON_HOME", home)
	if code := run([]string{"probe", t.TempDir(), filepath.Join(bin, "agy")}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("HOME holds %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(bin, "ran")); !os.IsNotExist(err) {
		t.Fatalf("agy ran before the build check: %v", err)
	}
	if code := run([]string{"probe", "x"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("probe x: exit %d", code)
	}
}

// INT or TERM ends a review without a report, whether the supervisor takes it
// while agy runs or it arrives during the cleanup after agy exited (Codex on
// #1113).
func TestReviewSignals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during bool
		sig    syscall.Signal
		code   int
	}{{"before the launch", false, syscall.SIGINT, 130}, {"during the cleanup", true, syscall.SIGTERM, 143}} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newReview(t)
			signals := make(chan os.Signal, 1)
			r.sup.signals = signals
			if tc.during {
				scan := r.sup.ops.scan
				r.sup.ops.scan = func(session int) ([]int, error) {
					select {
					case signals <- tc.sig:
					default:
					}
					return scan(session)
				}
			} else {
				signals <- tc.sig
			}
			var out bytes.Buffer
			if code, err := r.run(&out); code != tc.code || err == nil || out.Len() != 0 {
				t.Fatalf("code = %d, err = %v, output %q", code, err, out.String())
			}
			nothingLeft(t, r.source)
		})
	}
}

// Catching stops before the report is written: a signal that arrived just
// before still suppresses it, and none is caught during the write.
func TestReviewStopsCatchingBeforeTheReport(t *testing.T) {
	for _, late := range []bool{true, false} {
		r, _ := newReview(t)
		signals := make(chan os.Signal, 1)
		r.sup.signals = signals
		stopped := false
		r.stop = func() {
			stopped = true
			if late {
				signals <- syscall.SIGINT
			}
		}
		out := stoppedFirst{stopped: &stopped}
		code, err := r.run(&out)
		if late && (code != 130 || err == nil || out.Len() != 0) || !late && (code != 0 || err != nil || out.Len() == 0) {
			t.Fatalf("late = %v: code = %d, err = %v, output %q", late, code, err, out.String())
		}
		if out.early {
			t.Fatal("the report was written while signals were still caught")
		}
	}
}

// stoppedFirst is a buffer that notes a write before catching stopped.
type stoppedFirst struct {
	bytes.Buffer
	stopped *bool
	early   bool
}

func (b *stoppedFirst) Write(p []byte) (int, error) {
	b.early = b.early || !*b.stopped
	return b.Buffer.Write(p)
}

// INT or TERM during a report write that blocks, because nobody reads
// stdout, ends the launcher by that signal instead of being caught and lost
// (Codex on #1113). The test reads the start of the report, so the write has
// begun, then stops reading the remaining mebibyte and signals.
func TestReviewSignalDuringTheReport(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(executable, "-test.run=^TestReviewHelper$")
			cmd.Env = append(os.Environ(), "ARGONREVIEW_REVIEW_HELPER=1", "TMPDIR="+t.TempDir())
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			start := make([]byte, len("Argon review of"))
			if _, err := io.ReadFull(stdout, start); err != nil || string(start) != "Argon review of" {
				t.Fatalf("report start %q, %v", start, err)
			}
			time.Sleep(200 * time.Millisecond) // the rest fills the pipe
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, stdout)
			_ = cmd.Wait()
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != sig {
				t.Fatalf("the launcher ended with %v, not by %v", cmd.ProcessState, sig)
			}
		})
	}
}

func TestReviewHelper(t *testing.T) {
	if os.Getenv("ARGONREVIEW_REVIEW_HELPER") == "" {
		t.Skip("run by TestReviewSignalDuringTheReport")
	}
	r, fake := newReview(t)
	// Far more than a pipe holds, so the write blocks.
	write(t, filepath.Join(fake, "stdout"), `{"event":"result","result":{"status":"SUCCESS","response":"`+strings.Repeat("x", 1<<20)+`"}}`+"\n")
	code, _ := r.runCatching(os.Stdout)
	os.Exit(code)
}

// The shared settings are read only to hash; a change during the run fails it.
func TestReviewChecksTheSharedSettings(t *testing.T) {
	r, fake := newReview(t)
	write(t, filepath.Join(fake, "touch"), r.shared)
	var out bytes.Buffer
	if code, err := r.run(&out); code != 1 || err == nil || !strings.Contains(err.Error(), "shared Antigravity settings changed") || out.Len() != 0 {
		t.Fatalf("code = %d, err = %v, output %q", code, err, out.String())
	}
	nothingLeft(t, r.source)
}

func TestReviewStopsWhenPrepareRefuses(t *testing.T) {
	r, fake := newReview(t)
	r.prepare = func() error { return errors.New("refused") }
	if code, err := r.run(&bytes.Buffer{}); code != 1 || err == nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(fake, "seen")); !os.IsNotExist(err) {
		t.Fatalf("agy ran after prepare refused: %v", err)
	}
	nothingLeft(t, r.source)
}

// review runs only after the trusted-build check: an unstamped test binary
// is refused before gh or agy run.
func TestReviewChecksTheBuildFirst(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"gh", "agy"} {
		write(t, filepath.Join(bin, name), "#!/bin/sh\ntouch \""+bin+"/ran\"\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A usable HOME, so that prepare would write and gh run if review came
	// before the build check.
	home := resolved(t, t.TempDir())
	t.Setenv("RIBBITTO_ARGON_HOME", home)
	var stderr bytes.Buffer
	if code := run([]string{"review", "7"}, nil, &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "argonreview: ") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(bin, "ran")); !os.IsNotExist(err) {
		t.Fatalf("gh or agy ran before the build check: %v", err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("HOME holds %v, %v", entries, err)
	}
	if code := run([]string{"review", "07"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("review 07: exit %d", code)
	}
}
