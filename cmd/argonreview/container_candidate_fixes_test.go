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

func TestContainerBrowserLateRefusal(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%v", expired), func(t *testing.T) {
			v := browserRunFixture(t)
			now := v.browserSecurity.now()
			change := func() {
				if expired {
					v.browserSecurity.now = func() time.Time { return now.Add(browserSecurityAge) }
				} else {
					browserWrite(t, v.browserMode, "off", 0o400)
				}
			}
			change()
			early := containerExecution{browserRun: browserTestRun, admit: v.browserAdmission}
			want, err := runContainerExecution(context.Background(), v.worktree, "test-browser", "./internal/web", v.runner, v.copyParent, &early)
			if err != nil {
				t.Fatal(err)
			}
			wantFirst := browserPaused
			if expired {
				wantFirst = browserOverdue
			}
			if want != wantFirst+"\n"+containerCheckScope("test-browser")+"\n" {
				t.Fatalf("initial refusal line changed: %q", want)
			}
			browserWrite(t, v.browserMode, "on", 0o400)
			v.browserSecurity.now = func() time.Time { return now }
			held, released, reads, calls := false, false, 0, 0
			v.budget = fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
				held = true
				return budgetReservation{label: "0,1", memory: 2 * budgetUnit, release: func() { assertNoContainerCopy(t, v.copyParent); held, released = false, true }}, ""
			})
			execution := containerExecution{browserRun: browserTestRun, budget: v.budget}
			execution.admit = func() string {
				reads++
				if reads == 3 {
					dirs, err := os.ReadDir(v.copyParent)
					if err != nil || len(dirs) != 1 {
						t.Fatalf("late admission requires copy: %v %v", dirs, err)
					}
					if _, err := os.Stat(filepath.Join(v.copyParent, dirs[0].Name(), "tree", "tools", "go.mod")); err != nil {
						t.Fatal(err)
					}
					if execution.aggregation == nil || execution.runnerContext == nil {
						t.Fatal("test must exercise refusal with an initialized test2json aggregator")
					}
					change()
				}
				return v.browserAdmission()
			}
			execution.finishAggregation = func(a *containerAggregator, code int, err error, state aggregatorRunState) aggregatorResult {
				if execution.refused {
					t.Fatal("finishReport must not overwrite refusal")
				}
				return a.finish(code, err, state)
			}
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { calls++; return 0, nil }}
			report, err := runContainerExecution(context.Background(), v.worktree, "test-browser", "./internal/web", runner, v.copyParent, &execution)
			if calls != 0 {
				t.Fatalf("late refusal started container or cleanup: calls=%d", calls)
			}
			if err != nil || report != want || execution.summary != early.summary {
				t.Fatalf("finishReport overwrote refusal: report=%q want=%q summary=%q want=%q err=%v", report, want, execution.summary, early.summary, err)
			}
			first, _, _ := strings.Cut(execution.summary, "\n")
			if _, isError := verifyResult(execution.summary, nil, false, false); first != wantFirst || !isError {
				t.Fatalf("MCP refusal disagrees with host: %q", execution.summary)
			}
			if held || !released || reads != 3 || execution.aggregation != nil {
				t.Fatalf("reservation or aggregation retained: held=%v released=%v reads=%d", held, released, reads)
			}
			assertNoContainerCopy(t, v.copyParent)
		})
	}
}

func TestContainerFailureBodyWithStop(t *testing.T) {
	const diagnostic = "/tmp/argonreview-container-private/tree: Cannot connect to the Docker daemon"
	const minimal = "Details unavailable: the check did not complete."
	for _, stop := range []string{"cap", "malformed"} {
		failures := []string{"125", "126", "127", "signal", "signal-status", "runner-error"}
		if stop == "cap" {
			failures = append(failures, "cleanup", "parent", "deadline", "own-stop", "own-cancel-error")
		}
		for _, failure := range failures {
			t.Run(stop+"/"+failure, func(t *testing.T) {
				t.Parallel()
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				execution := containerExecution{timeout: containerTimeout}
				if failure == "deadline" {
					execution.timeout = 2 * time.Second
				}
				code := 0
				var runErr error
				switch failure {
				case "125":
					code = 125
				case "126":
					code = 126
				case "127":
					code = 127
				case "signal":
					code = -1
					runErr = errors.New("signal: killed")
				case "signal-status":
					code = -1
				case "runner-error":
					runErr = errors.New("runner failed")
				case "own-cancel-error":
					code = -1
					runErr = context.Canceled
				}
				runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] != "run" {
						if args[0] == "rm" && failure == "cleanup" {
							return 1, errors.New(diagnostic)
						}
						return 0, nil
					}
					_, _ = io.WriteString(stderr, diagnostic+"\n")
					_, _ = io.WriteString(out, containerJSON("p", "start"))
					if stop == "cap" {
						wireCap(t, execution.aggregation, out, "total output")
					} else {
						_, _ = io.WriteString(out, "garbage\n")
					}
					if failure == "parent" {
						cancel()
					}
					if failure == "deadline" {
						<-execution.parent.Done()
					}
					return code, runErr
				}}
				// Use an explicit parent deadline so the wait is independent of the
				// aggregator's immediate cancellation of its runner context.
				if failure == "deadline" {
					var expire context.CancelFunc
					parent, expire = context.WithTimeout(parent, 2*time.Second)
					defer expire()
				}
				report, _ := runContainerExecution(parent, containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
				want := "FAILED: malformed test stream"
				if stop == "cap" {
					want = "NOT VERIFIED: output limit exceeded (output cut off)"
				} else if failure == "deadline" {
					want = containerTimeLimitVerdict
				}
				first, _, _ := strings.Cut(report, "\n")
				summaryFirst, _, _ := strings.Cut(execution.summary, "\n")
				if first != want || summaryFirst != want {
					t.Fatalf("first line changed: report=%q summary=%q want=%q", first, summaryFirst, want)
				}
				own := strings.HasPrefix(failure, "own-")
				if strings.Contains(execution.summary, minimal) == own {
					t.Fatalf("wrong failure body: %s", execution.summary)
				}
				for _, text := range []string{"Standard error:", "/tmp/argonreview-container-private", "Cannot connect to the Docker daemon"} {
					if strings.Contains(execution.summary, text) {
						t.Fatalf("MCP leaked client diagnostic %q: %s", text, execution.summary)
					}
				}
				if own && stop == "cap" {
					for _, text := range []string{"Counts unavailable: output limit exceeded; the stream was cut.", "Limit reached: total output"} {
						if !strings.Contains(execution.summary, text) {
							t.Fatalf("own cap body changed: %s", execution.summary)
						}
					}
				}
			})
		}
	}
}

func TestContainerSummaryStderrNotServable(t *testing.T) {
	a := aggregatorFixture("")
	_, _ = io.WriteString(a.stderrWriter(), "Cannot connect to the Docker daemon\n")
	aggregatorWrite(a, "garbage\n")
	a.finish(126, nil, aggregatorRunState{completed: true})
	in := summaryInput{check: "test", pattern: ".", scope: "fixture", verdict: "FAILED: malformed test stream", facts: recordSummaryFacts(a.record)}
	if got := buildContainerSummary(in); strings.Contains(got, "Cannot connect") || strings.Contains(got, "Standard error:") {
		t.Fatalf("not-servable stderr printed: %s", got)
	}
}

func TestBrowserRecordInitWhileOn(t *testing.T) {
	b, path, entry := browserRecordFixture(t)
	browserWrite(t, path, "on", 0o400)
	log := browserRecordCall(t, b, browserRecordArgs(entry, true), 1)
	if log != "browser-check-record: initial check cannot be recorded while browser-mode is on\n" {
		t.Fatalf("init refusal: %q", log)
	}
}

func TestContainerCheckBrowserModeOnlyForBrowser(t *testing.T) {
	for _, check := range verifyCheckNames() {
		if _, _, err := parseContainerCheck([]string{"/tree", check, ".", "--browser-mode", "/trusted/browser-mode"}); err == nil {
			t.Fatalf("non-browser check %s accepted browser-mode", check)
		}
	}
	if _, _, err := parseContainerCheck([]string{"/tree", "test-browser", "./internal/web", browserTestRun, "--browser-mode", "/trusted/browser-mode"}); err != nil {
		t.Fatal(err)
	}
}

func TestContainerCommandCapFailureBody(t *testing.T) {
	flood := strings.Repeat("x", containerOutputLimit+1)
	for _, check := range []string{"build", "vet", "generated"} {
		for _, failure := range []string{"own-stop", "own-cancel-error", "runner", "125", "cleanup", "parent"} {
			t.Run(check+"/"+failure, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				execution := containerExecution{}
				runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
					if args[0] == "rm" && failure == "cleanup" {
						return 1, errors.New("cleanup failed")
					}
					if args[0] != "run" {
						return 0, nil
					}
					_, _ = io.WriteString(stderr, "/tmp/argonreview-container-private: Cannot connect to the Docker daemon\n")
					_, _ = io.WriteString(out, flood)
					switch failure {
					case "parent":
						cancel()
					case "runner":
						return -1, errors.New("signal: killed")
					case "125":
						return 125, nil
					case "own-cancel-error":
						return -1, context.Canceled
					}
					return 0, nil
				}}
				report, _ := runContainerExecution(parent, containerFixture(t), check, "./...", runner, t.TempDir(), grantContainerExecution(&execution))
				const first = "NOT VERIFIED: output limit exceeded (output cut off)\n"
				if !strings.HasPrefix(report, first) || !strings.HasPrefix(execution.summary, first) {
					t.Fatalf("command cap verdict changed: %s / %s", report, execution.summary)
				}
				own := strings.HasPrefix(failure, "own-")
				if strings.Contains(execution.summary, "Details unavailable: the check did not complete.") == own || strings.Contains(execution.summary, "Counts unavailable: output limit exceeded; the stream was cut.") != own {
					t.Fatalf("command cap failure body: %s", execution.summary)
				}
				for _, text := range []string{"Standard error:", "/tmp/argonreview-container-private", "Cannot connect to the Docker daemon"} {
					if strings.Contains(execution.summary, text) {
						t.Fatalf("command MCP leaked client diagnostic: %s", execution.summary)
					}
				}
			})
		}
	}
}

func TestContainerRunnerCancellationProvenance(t *testing.T) {
	dir := slowDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		code, err := (dockerRunner{ownGroup: true, path: dir}).run(ctx, []string{"run"}, []string{"PATH=/usr/bin:/bin"}, io.Discard, io.Discard)
		if code != -1 {
			t.Errorf("canceled client status=%d, want signal status -1", code)
		}
		done <- err
	}()
	waitFor(t, filepath.Join(dir, "pid"))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("own cancellation provenance lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake client did not stop")
	}
	deadDockerChild(t, dir)
}
