package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type logExecutionVerifier struct {
	execution          containerExecution
	tree, copies, host string
	runner             containerRunner
}

func (v *logExecutionVerifier) verify(ctx context.Context, c, p string) (string, error) {
	summary, _, _, err := v.verifyRecorded(ctx, c, p, "")
	return summary, err
}
func (v *logExecutionVerifier) verifyRecorded(ctx context.Context, c, p, r string) (string, *containerRecord, bool, error) {
	v.execution.browserRun = r
	var err error
	v.host, err = runContainerExecution(ctx, v.tree, c, p, v.runner, v.copies, &v.execution)
	return v.execution.summary, v.execution.record, v.execution.runnerFailed, err
}

func TestVerifyLogExecutionFinalVerdict(t *testing.T) {
	for _, check := range []string{"test", "build"} {
		for _, outcome := range []string{"failed", "skipped", "verified", "timeout", "cancelled", "runner error", "exit 125", "exit 126", "exit 127", "source cleanup", "container cleanup"} {
			if check == "build" && outcome == "skipped" {
				continue
			}
			for _, logs := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/logs=%v", check, outcome, logs), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					v := &logExecutionVerifier{execution: containerExecution{timeout: containerTimeout, logs: logs, budget: grantContainerBudget()}, tree: containerFixture(t), copies: t.TempDir()}
					execution := &v.execution
					if outcome == "timeout" {
						execution.timeout = time.Second
					}
					runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, diagnostic io.Writer) (int, error) {
						if args[0] == "rm" && outcome == "container cleanup" {
							return 1, errors.New("removal failed")
						}
						if args[0] != "run" {
							return 0, nil
						}
						state := "pass"
						switch outcome {
						case "failed":
							state = "fail"
						case "skipped":
							state = "skip"
						}
						if check == "test" {
							_, _ = io.WriteString(out, containerEvents("p", "TestOne", state))
						}
						_, _ = io.WriteString(diagnostic, "runner diagnostic\n")
						switch outcome {
						case "failed":
							return 1, nil
						case "timeout":
							<-ctx.Done()
							return -1, ctx.Err()
						case "cancelled":
							cancel()
							<-ctx.Done()
							return -1, ctx.Err()
						case "runner error":
							return 0, errors.New("runner failed")
						case "exit 125":
							return 125, nil
						case "exit 126":
							return 126, nil
						case "exit 127":
							return 127, nil
						case "source cleanup":
							for _, arg := range args {
								if !strings.HasSuffix(arg, ",dst=/work,readonly") {
									continue
								}
								dir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
								if err := os.Chmod(dir, 0o500); err != nil {
									t.Error(err)
								}
								t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.RemoveAll(filepath.Dir(dir)) })
							}
						}
						return 0, nil
					}}
					v.runner = runner
					var store *verifyLogStore
					if logs {
						store = &verifyLogStore{}
					}
					call := strings.Replace(mcpCall, `"check":"test"`, `"check":"`+check+`"`, 1)
					text, bad := logMCPCall(t, verifyMCPServer{verifier: v, logs: store}, ctx, call, nil)
					wantFirst := "FAILED"
					switch outcome {
					case "verified":
						wantFirst = "verified"
					case "skipped":
						wantFirst = "NOT VERIFIED: 1 skipped"
					case "timeout":
						wantFirst = containerTimeLimitVerdict
					case "source cleanup":
						wantFirst = "FAILED: source cleanup failed"
					}
					first, _, _ := strings.Cut(text, "\n")
					if first != wantFirst || bad != (outcome != "verified") {
						t.Fatalf("verdict %q, bad=%v", text, bad)
					}
					assertLogCapture(t, store, text, v.execution.record, logs && bad)
					minimal := outcome != "verified" && outcome != "failed" && outcome != "skipped"
					if strings.Contains(text, "Details unavailable:") != minimal {
						t.Fatalf("minimal body: %q", text)
					}
					if execution.runnerFailed != minimal {
						t.Fatalf("runner failure flag = %v, want %v", execution.runnerFailed, minimal)
					}
					if minimal {
						if (logs || outcome != "source cleanup" && outcome != "container cleanup") && !v.execution.record.stderrNotServable {
							t.Fatal("runner failure retained servable stderr")
						}
						if logs {
							if _, _, class := store.captures[0].unit(verifyLogArguments{stream: "stderr"}); class != verifyLogStderrDenied {
								t.Fatal("runner stderr served")
							}
						}
						if logs {
							// Removing just the ID must leave the existing fixed minimal body.
							c := store.captures[0]
							without := strings.Replace(text, verifyLogRunPrefix+c.id+"\n", "", 1)
							old, _ := verifyResult(v.execution.summary, nil, false, false)
							// Execution errors are appended by the wrapper, outside the minimal body.
							if !strings.HasPrefix(without, old) {
								t.Fatalf("minimal body changed: %q, want prefix %q", without, old)
							}
						}
					}
					if !logs {
						// Legacy wrapper plus the undecorated production summary is the old path.
						// No runner error text is needed for this exact-byte check.
						if outcome == "failed" || outcome == "skipped" || outcome == "verified" {
							old, _ := verifyResult(v.execution.summary, nil, false, false)
							if text != old {
								t.Fatal("logs-off result bytes changed")
							}
						}
					}
				})
			}
		}
	}
}

func TestVerifyLogCleanupCapture(t *testing.T) {
	for _, tc := range []struct{ check, stop string }{
		{"test", "none"}, {"test", "malformed"}, {"test", "failed-cap"},
		{"build", "none"}, {"build", "output-cap"},
	} {
		for _, cleanup := range []string{"source", "container"} {
			t.Run(tc.check+"/"+tc.stop+"/"+cleanup, func(t *testing.T) {
				execution := containerExecution{logs: true, budget: grantContainerBudget()}
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] != "run" {
						if args[0] == "rm" && cleanup == "container" {
							return 1, errors.New("removal failed")
						}
						return 0, nil
					}
					if cleanup == "source" {
						for _, arg := range args {
							if !strings.HasSuffix(arg, ",dst=/work,readonly") {
								continue
							}
							dir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
							if err := os.Chmod(dir, 0o500); err != nil {
								t.Error(err)
							}
							t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.RemoveAll(filepath.Dir(dir)) })
						}
					}
					_, _ = io.WriteString(stderr, "benign diagnostic\n")
					switch tc.stop {
					case "none":
						if tc.check == "test" {
							_, _ = io.WriteString(out, containerEvents("p", "TestOne", "pass"))
						}
						return 0, nil
					case "malformed":
						_, _ = io.WriteString(out, "garbage\n")
					case "failed-cap":
						_, _ = io.WriteString(out, containerJSON("p", "start", "run/TestBad", "fail/TestBad"))
						wireCap(t, execution.aggregation, out, "total output")
					case "output-cap":
						_, _ = io.WriteString(out, strings.Repeat("x", containerOutputLimit+1))
					}
					if ctx.Err() != context.Canceled {
						t.Error("own stop did not cancel runner")
					}
					return -1, context.Canceled
				}}
				host, err := runContainerExecution(context.Background(), containerFixture(t), tc.check, ".", runner, t.TempDir(), &execution)
				if err == nil || !strings.Contains(err.Error(), "remov") || execution.ownCancel != (tc.stop != "none") {
					t.Fatalf("cleanup/own stop fixture: own=%v err=%v", execution.ownCancel, err)
				}
				if tc.check == "test" && (execution.record.stderrNotServable || strings.Contains(host, "Stderr is not servable")) {
					t.Fatal("cleanup changed execution-level stderr")
				}
				if !execution.runnerFailed {
					t.Fatal("cleanup failure lost runner failure flag")
				}
				before := execution.record.stderrNotServable
				_, _ = verifyResult(execution.summary, err, false, false)
				if execution.record.stderrNotServable != before {
					t.Fatal("logs-off wrapper changed execution-level stderr")
				}
				store := &verifyLogStore{}
				text, bad := verifyRecordedResult(execution.summary, err, false, false, execution.runnerFailed, store, execution.record)
				assertLogCapture(t, store, text, execution.record, true)
				if !bad || !strings.Contains(text, "Details unavailable: the check did not complete.") {
					t.Fatalf("cleanup lost minimal failure body: %q", text)
				}
				if _, _, class := store.captures[0].unit(verifyLogArguments{stream: "stderr"}); class != verifyLogStderrDenied {
					t.Fatal("cleanup capture served stderr")
				}
			})
		}
	}
}

func TestVerifyLogContainerVerifierRecord(t *testing.T) {
	for _, check := range verifyCheckNames() {
		t.Run(check, func(t *testing.T) {
			v := containerVerifier{worktree: containerFixture(t), runner: summaryRunner(containerEvents("p", "TestBad", "fail"), "diagnostic", 1), copyParent: t.TempDir(), timeout: containerTimeout, budget: grantContainerBudget(), logs: true}
			pattern, run := "./...", ""
			if check == verifyBrowserCheck {
				v = browserRunFixture(t)
				v.logs = true
				pattern, run = "./internal/web", "TestOneBrowser"
				v.runner = summaryRunner(containerEvents("github.com/tkakkie/ribbitto/internal/web", run, "fail"), "diagnostic", 1)
			}
			summary, r, runnerFailed, err := v.verifyRecorded(context.Background(), check, pattern, run)
			if err != nil || r == nil || !r.sealed || summary == "" || runnerFailed {
				t.Fatalf("record path: %q, %v, %v", summary, r, err)
			}
			if check == verifyBrowserCheck {
				legacy, err := v.verifyBrowser(context.Background(), pattern, run)
				if err != nil || legacy != summary {
					t.Fatal("browser path changed")
				}
			}
		})
	}
	for _, outcome := range []string{"budget", "browser admission", "validation", "copy"} {
		t.Run("nil/"+outcome, func(t *testing.T) {
			v := containerVerifier{worktree: containerFixture(t), copyParent: t.TempDir(), budget: grantContainerBudget(), logs: true}
			check, pattern, run := "test", ".", ""
			switch outcome {
			case "budget":
				v.budget = nil
			case "browser admission":
				check, pattern, run = "test-browser", "./internal/web", "TestOneBrowser"
			case "validation":
				pattern = "-x"
			case "copy":
				v.worktree = filepath.Join(t.TempDir(), "missing")
			}
			summary, r, runnerFailed, err := v.verifyRecorded(context.Background(), check, pattern, run)
			store := &verifyLogStore{}
			text, _ := verifyRecordedResult(summary, err, false, false, runnerFailed, store, r)
			if r != nil {
				t.Fatal("pre-record outcome returned a record")
			}
			assertLogCapture(t, store, text, r, false)
		})
	}
}

func TestVerifyLogRefusalCleanupNoRecord(t *testing.T) {
	ctx := context.Background()
	result := containerExecution{logs: true, aggregation: newContainerAggregator(nil, nil), runnerContext: ctx}
	result.refuse("test-browser", "./internal/web", browserPaused)
	report, err := result.finishReport(ctx, "FAILED: source cleanup failed\n", errors.New("source removal failed"), summaryInput{check: "test-browser", runnerFailed: true})
	if err == nil || !strings.HasPrefix(report, "FAILED: source cleanup failed\n") || result.record != nil || result.aggregation != nil {
		t.Fatal("pre-start refusal became a recorded run")
	}
	store := &verifyLogStore{}
	text, _ := verifyRecordedResult(result.summary, err, false, false, result.runnerFailed, store, result.record)
	assertLogCapture(t, store, text, nil, false)
}
