package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestContainerOwnCancelPresentation(t *testing.T) {
	for _, stop := range []string{"malformed", "failed-cap"} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cleanup=%v", stop, cleanup), func(t *testing.T) {
				execution := containerExecution{}
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] != "run" {
						if args[0] == "rm" && cleanup {
							return 1, errors.New("rm failed")
						}
						return 0, nil
					}
					_, _ = io.WriteString(stderr, "benign diagnostic\n")
					if stop == "malformed" {
						_, _ = io.WriteString(out, "garbage\n")
					} else {
						_, _ = io.WriteString(out, containerJSON("p", "start", "run/TestBad", "fail/TestBad"))
						wireCap(t, execution.aggregation, out, "total output")
					}
					if ctx.Err() != context.Canceled {
						t.Fatal("own stop did not cancel runner")
					}
					return -1, context.Canceled
				}}
				report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
				if !execution.ownCancel || !execution.runState.completed || execution.record.stderrNotServable || strings.Contains(report, "Stderr is not servable") {
					t.Fatalf("own stop lost completion/stderr: own=%v state=%+v report=%s", execution.ownCancel, execution.runState, report)
				}
				text, bad := verifyResult(execution.summary, err, false, false)
				if !bad || strings.Contains(text, "context canceled") {
					t.Fatalf("MCP own stop classification: %s", text)
				}
				if cleanup {
					if err == nil || !strings.Contains(err.Error(), "container removal failed") || strings.Contains(err.Error(), "may never have existed") || !strings.Contains(text, "Details unavailable: the check did not complete.") || strings.Contains(text, "Standard error:") || !strings.HasPrefix(text, "FAILED") {
						t.Fatalf("cleanup failure lost precedence/wording: err=%v text=%s", err, text)
					}
				} else if err != nil || text != execution.summary || !strings.Contains(text, "Standard error:\n  | benign diagnostic\n") {
					t.Fatalf("own stop changed ordinary MCP body: err=%v text=%s", err, text)
				}
			})
		}
	}
}

func TestContainerCommandCapCancellation(t *testing.T) {
	for _, timing := range []string{"own", "cleanup", "parent", "deadline"} {
		t.Run(timing, func(t *testing.T) {
			t.Parallel()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			execution := containerExecution{timeout: containerTimeout}
			if timing == "deadline" {
				execution.timeout = 2 * time.Second
			}
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
				if args[0] != "run" {
					if args[0] == "rm" && timing == "cleanup" {
						return 1, errors.New("rm failed")
					}
					return 0, nil
				}
				_, _ = io.WriteString(out, strings.Repeat("x", containerOutputLimit+1))
				if ctx.Err() != context.Canceled {
					t.Fatal("cap did not cancel runner child")
				}
				switch timing {
				case "parent":
					cancel()
				case "deadline":
					// The cap must leave the run timer alive. After it expires,
					// interruption rather than the cap owns the MCP body.
					<-time.After(execution.timeout + 100*time.Millisecond)
				}
				return -1, context.Canceled
			}}
			report, err := runContainerExecution(parent, containerFixture(t), "build", "./...", runner, t.TempDir(), grantContainerExecution(&execution))
			if !strings.HasPrefix(report, "NOT VERIFIED: output limit exceeded") {
				t.Fatal(report)
			}
			minimal := timing != "own"
			if strings.Contains(execution.summary, "Details unavailable: the check did not complete.") != minimal {
				t.Fatalf("cap then %s body: %s", timing, execution.summary)
			}
			if (timing == "own" || timing == "cleanup") != execution.ownCancel {
				t.Fatalf("cap then %s misclassified own cancellation: %v", timing, execution.ownCancel)
			}
			if timing == "own" && err != nil {
				t.Fatalf("own cap returned %v", err)
			}
			if timing == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cap disarmed run deadline: %v", err)
			}
			if timing == "cleanup" && (err == nil || !strings.Contains(err.Error(), "container removal failed") || strings.Contains(err.Error(), "may never have existed")) {
				t.Fatalf("own command cap cleanup wording: %v", err)
			}
		})
	}
}

func TestContainerCancellationAtReturn(t *testing.T) {
	for _, timing := range []string{"no-stop", "parent", "deadline"} {
		t.Run(timing, func(t *testing.T) {
			t.Parallel()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			execution := containerExecution{timeout: containerTimeout}
			if timing == "deadline" {
				execution.timeout = 2 * time.Second
			}
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
				if args[0] != "run" {
					return 0, nil
				}
				_, _ = io.WriteString(stderr, "benign diagnostic\n")
				if timing != "no-stop" {
					_, _ = io.WriteString(out, "garbage\n")
				}
				switch timing {
				case "parent":
					cancel()
				case "deadline":
					<-time.After(execution.timeout + 100*time.Millisecond)
				}
				return -1, context.Canceled
			}}
			report, err := runContainerExecution(parent, containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
			if execution.ownCancel || !errors.Is(err, context.Canceled) || !execution.record.stderrNotServable || !strings.Contains(report, "Stderr is not servable") || !strings.Contains(execution.summary, "Details unavailable: the check did not complete.") {
				t.Fatalf("independent cancellation misclassified: own=%v err=%v report=%s summary=%s", execution.ownCancel, err, report, execution.summary)
			}
		})
	}
}

func TestContainerLateRefusalSourceCleanup(t *testing.T) {
	// Reproduce the deferred source-removal callback's input after the final
	// admission refuses. Permissions cannot reliably force RemoveAll to fail
	// on every test host, so exercise its existing finalisation seam directly.
	execution := containerExecution{aggregation: newContainerAggregator(func() {}, nil)}
	_ = execution.refuse("test-browser", "./internal/web", browserPaused)
	in := summaryInput{check: "test-browser", pattern: "./internal/web", scope: containerCheckScope(""), runnerFailed: true}
	const report = "FAILED: source cleanup failed\ntest-browser check result could not be confirmed.\n"
	cleanupErr := errors.New("removing source copy: denied")
	got, err := execution.finishReport(context.Background(), report, cleanupErr, in)
	text, bad := verifyResult(execution.summary, err, false, false)
	if got != report || !errors.Is(err, cleanupErr) || !bad || !strings.HasPrefix(text, "FAILED: source cleanup failed\n") || !strings.Contains(text, "Details unavailable: the check did not complete.") || strings.Contains(text, browserPaused) {
		t.Fatalf("source cleanup must precede refusal: host=%q text=%q err=%v", got, text, err)
	}
}

func TestContainerClientSignalProvenance(t *testing.T) {
	for _, signal := range []string{"TERM", "ABRT", "KILL"} {
		cmd := exec.Command("/bin/sh", "-c", "kill -"+signal+" $$")
		var exit *exec.ExitError
		if !errors.As(cmd.Run(), &exit) {
			t.Fatal("fixture did not exit by signal")
		}
		for _, killed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/kill=%v", signal, killed), func(t *testing.T) {
				if got := containerClientOwnCancel(exit, killed); got != (killed && signal == "KILL") {
					t.Fatalf("signal %s, our kill=%v: own cancellation=%v", signal, killed, got)
				}
			})
		}
	}
}

type containerSignalWriter struct {
	t      *testing.T
	cancel context.CancelFunc
	buffer strings.Builder
}

func (w *containerSignalWriter) Write(p []byte) (int, error) {
	_, _ = w.buffer.Write(p)
	if !strings.Contains(w.buffer.String(), "\n") {
		return len(p), nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(w.buffer.String()))
	if err != nil {
		w.t.Error(err)
		return len(p), nil
	}
	// The client signals itself before this callback cancels its own stream.
	// Hold the copy goroutine until Wait reaps it: our kill cannot fire on a
	// reaped process, even if the context watcher has not yet returned.
	until := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(until) {
			w.t.Error("independently signaled client was not reaped")
			break
		}
		time.Sleep(time.Millisecond)
	}
	w.cancel()
	return len(p), nil
}

func TestContainerRunnerIndependentSignalDuringStop(t *testing.T) {
	for _, signal := range []string{"TERM", "KILL"} {
		t.Run(signal, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "docker")
			write(t, path, "#!/bin/sh\necho $$\nkill -"+signal+" $$\n")
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := newContainerAggregator(cancel, nil)
			writer := &containerSignalWriter{t: t, cancel: func() { _, _ = io.WriteString(a.stdoutWriter(), "garbage\n") }}
			code, err := (dockerRunner{path: dir}).run(ctx, nil, dockerEnvironment(), writer, io.Discard)
			if ctx.Err() != context.Canceled || a.record.stop != stopMalformed || code != -1 || err != nil {
				t.Fatalf("independent signal gained own-kill provenance: ctx=%v stop=%v code=%d err=%v", ctx.Err(), a.record.stop, code, err)
			}
		})
	}
}

func TestContainerRunnerOwnStopKill(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(fmt.Sprintf("group=%v", group), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "docker")
			// A shell loop has no descendant that can keep stdout open after
			// the default single-process Cancel kills this fake client.
			write(t, path, "#!/bin/sh\nprintf 'garbage\\n'\nwhile :; do :; done\n")
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runnerCtx, stop := context.WithCancel(ctx)
			defer stop()
			a := newContainerAggregator(stop, nil)
			code, err := (dockerRunner{ownGroup: group, path: dir}).run(runnerCtx, nil, dockerEnvironment(), a.stdoutWriter(), a.stderrWriter())
			if ctx.Err() != nil || a.record.stop != stopMalformed || code != -1 || !errors.Is(err, context.Canceled) {
				t.Fatalf("own stop lost kill provenance: ctx=%v stop=%v code=%d err=%v", ctx.Err(), a.record.stop, code, err)
			}
		})
	}
}
