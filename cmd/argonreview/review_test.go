package main

import (
	"bytes"
	"errors"
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
// pull request 7, prints dir/stdout and dir/stderr, appends to the
// file named in dir/touch, sleeps if dir/sleep exists, and exits with
// dir/status.
func fakeAgy(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
{ echo "HOME=$HOME"; echo "PWD=$(pwd -P)"; for a in "$@"; do echo "ARG=$a"; done; } > '` + dir + `/seen'
[ -f "$HOME/prepared" ] || exit 9
[ "$(cat README)" = two ] && [ -z "$(find . -iname .agents)" ] || exit 8
cat '` + dir + `/stdout'
cat '` + dir + `/stderr' >&2
if [ -f '` + dir + `/touch' ]; then echo changed >> "$(cat '` + dir + `/touch')"; fi
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

// newReview returns a review of pull request 7 of pullRequests' fixture, run
// by fakeAgy, with the fake's directory. The dedicated HOME, the fake and the
// account's shared settings live outside the fixture's TMPDIR.
func newReview(t *testing.T) (review, string) {
	t.Helper()
	fake, home, account := t.TempDir(), resolved(t, t.TempDir()), t.TempDir()
	shared := filepath.Join(account, sharedSettings)
	write(t, shared, "{}\n")
	agy := fakeAgy(t, fake)
	source, revision, pr := pullRequests(t)
	return review{
		source: source, revision: revision, remote: originOf(source), number: 7,
		home: home, shared: shared, agy: agy,
		prepare: func() error { write(t, filepath.Join(home, "prepared"), ""); return nil },
		read:    func(int) (target, error) { return pr, nil },
		git:     rawGit,
		sup:     supervisor{10 * time.Second, time.Second, nil, systemCleanup()},
	}, fake
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
	pr, _ := r.read(7)
	if want := "Argon review of tkakkie/ribbitto#7 at " + pr.Head + "\nLauncher built from " + r.revision + "\n\nNo findings.\n"; out.String() != want {
		t.Fatalf("report:\n%s\nwant:\n%s", out.String(), want)
	}
	data, err := os.ReadFile(filepath.Join(fake, "seen"))
	if err != nil {
		t.Fatal(err)
	}
	seen := string(data)
	for _, want := range []string{
		"HOME=" + r.home + "\n", "PWD=" + os.Getenv("TMPDIR") + "/argonreview-", "/tree\nARG=-p\n",
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
