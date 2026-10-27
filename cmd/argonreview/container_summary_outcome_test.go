package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func summaryUnconfirmedStream() string {
	return containerJSON("p", "start", "run/TestPass", "pass/TestPass", "run/TestUnconfirmed") +
		testOutput("p", "TestUnconfirmed", "unconfirmed excerpt\na.go:3: undefined: x\npanic: broken\n") +
		containerJSON("p", "fail/TestUnconfirmed", "fail")
}

func assertIncompleteSummary(t *testing.T, report, summary string, err error) {
	t.Helper()
	first, _, _ := strings.Cut(report, "\n")
	text, bad := verifyResult(summary, err, false, false)
	const body = "Details unavailable: the check did not complete.\nOmitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"
	lines := strings.SplitN(summary, "\n", 4)
	if !bad || len(lines) != 4 || lines[0] != first || !strings.HasPrefix(text, first+"\n") || !strings.HasPrefix(lines[1], "Check: ") || !strings.HasPrefix(lines[2], "Scope: ") || lines[3] != body {
		t.Fatalf("incomplete execution: summary %q, MCP %q, error %v", summary, text, err)
	}
	for _, forbidden := range []string{"Packages:", "Leaf tests:", "TestUnconfirmed", "unconfirmed excerpt", "undefined: x", "panic: broken", "/host/private/worktree"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("incomplete MCP result contains %q: %s", forbidden, text)
		}
	}
}

func TestContainerSummaryExecutionFailure(t *testing.T) {
	for _, check := range verifyCheckNames() {
		for _, outcome := range []string{"runner error", "docker 125", "timeout", "cancelled", "container removal"} {
			t.Run(check+"/"+outcome, func(t *testing.T) {
				run := func(mcp bool) (string, string, error) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, diagnostic io.Writer) (int, error) {
						if args[0] == "rm" && outcome == "container removal" {
							return 1, nil
						}
						if args[0] != "run" {
							return 0, nil
						}
						_, _ = io.WriteString(out, summaryUnconfirmedStream())
						_, _ = io.WriteString(diagnostic, "/host/private/worktree: docker diagnostic\n")
						switch outcome {
						case "runner error":
							return 0, errors.New("runner failed")
						case "docker 125":
							return 125, nil
						case "timeout":
							return 0, context.DeadlineExceeded
						case "cancelled":
							cancel()
						}
						return 0, nil
					}}
					tree, parent := containerFixture(t), t.TempDir()
					if mcp {
						summary, err := (containerVerifier{grantContainerBudget(), tree, runner, parent, containerTimeout}).verify(ctx, check, "./...")
						return "", summary, err
					}
					var execution containerExecution
					report, err := runContainerExecution(ctx, tree, check, "./...", runner, parent, grantContainerExecution(&execution))
					return report, execution.summary, err
				}
				report, summary, err := run(false)
				if !strings.HasPrefix(report, "FAILED\n") || !strings.Contains(report, summaryUnconfirmedStream()) || !strings.Contains(report, "/host/private/worktree") || (err == nil) != (outcome == "docker 125") {
					t.Fatalf("host report changed: %q, error %v", report, err)
				}
				if outcome == "timeout" && !errors.Is(err, context.DeadlineExceeded) || outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("missing context error: %v", err)
				}
				assertIncompleteSummary(t, report, summary, err)
				_, mcpSummary, mcpErr := run(true)
				if mcpSummary != summary || (mcpErr == nil) != (err == nil) {
					t.Fatal("MCP did not use the captured summary")
				}
				assertIncompleteSummary(t, report, mcpSummary, mcpErr)
			})
		}
	}
}

func TestContainerSummaryLateNonverifiedContext(t *testing.T) {
	for _, tc := range []struct{ name, stream, first, detail string }{
		{"failed", summaryUnconfirmedStream(), "FAILED", "unconfirmed excerpt"},
		{"skipped", containerEvents("p", "TestUnconfirmed", "skip"), "NOT VERIFIED: 1 skipped", "Skipped names:"},
	} {
		for _, interruption := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(tc.name+"/"+interruption.Error(), func(t *testing.T) {
				var execution containerExecution
				report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", summaryRunner(tc.stream, "/host/private/worktree", 0), t.TempDir(), grantContainerExecution(&execution))
				if err != nil || !strings.HasPrefix(report, tc.first+"\n") || !strings.Contains(execution.summary, tc.detail) {
					t.Fatalf("ordinary check failure lost diagnostics: %q, %v", execution.summary, err)
				}
				// Replay only deferred finalisation to place interruption deterministically
				// after the real runner has produced its non-verified report.
				ctx := context.Background()
				if interruption == context.Canceled {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				} else {
					expired, cancel := context.WithDeadline(ctx, time.Time{})
					defer cancel()
					ctx = expired
				}
				in := summaryFixture("test", tc.first, "", "")
				in.pattern = "."
				final, err := execution.finishReport(ctx, report, nil, in)
				if final != report || !errors.Is(err, interruption) {
					t.Fatalf("late interruption changed host report: %q, %v", final, err)
				}
				assertIncompleteSummary(t, final, execution.summary, err)
			})
		}
	}
}

func TestContainerSummaryMalformedGeneratedDiff(t *testing.T) {
	const raw = "malformed diff content\n+unvalidated addition\n"
	v := containerVerifier{grantContainerBudget(), containerFixture(t), summaryRunner(raw, "", generatedDiffer), t.TempDir(), containerTimeout}
	report, hostErr := runContainerCheck(context.Background(), v.worktree, "generated", "./...", v.runner, v.copyParent, grantContainerBudget())
	summary, err := v.verify(context.Background(), "generated", "./...")
	text, bad := verifyResult(summary, err, false, false)
	if hostErr != nil || err != nil || !bad || !strings.HasPrefix(report, "FAILED: generated files differ\nDiff stat: diff was not understood\n") || !strings.Contains(report, raw) || !strings.HasPrefix(text, strings.Split(report, "\n")[0]+"\n") {
		t.Fatalf("generated verdict or host report changed: %q, MCP %q, errors %v/%v", report, text, hostErr, err)
	}
	if !strings.Contains(text, "\nDiff stat: diff was not understood.\n") || !strings.Contains(text, "; 2 lines not shown;") || strings.Contains(text, "malformed diff content") || strings.Contains(text, "unvalidated addition") || strings.Contains(text, "\nDiff:\n") || strings.Contains(text, "Generated:") {
		t.Fatalf("malformed diff was not diagnosed and omitted: %s", text)
	}
}

func TestContainerTimeoutConfiguration(t *testing.T) {
	production := newContainerVerifier("worktree")
	if production.worktree != "worktree" || production.timeout != containerTimeout || production.copyParent != containerCopyParent || production.runner != (dockerRunner{ownGroup: true}) {
		t.Fatalf("production verifier configuration: %+v", production)
	}
	for _, path := range []string{"host", "summary", "production verifier", "zero execution", "injected execution", "injected verifier"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			tree, copies := containerFixture(t), t.TempDir()
			limit := containerTimeout
			if strings.HasPrefix(path, "injected") {
				limit = 2 * time.Second
			}
			called := false
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, _, _ io.Writer) (int, error) {
				if args[0] != "run" {
					return 0, nil
				}
				called = true
				deadline, ok := ctx.Deadline()
				if remaining := time.Until(deadline); !ok || remaining < limit-time.Minute || remaining > limit {
					t.Fatalf("runner deadline remaining %v, want [%v, %v]", remaining, limit-time.Minute, limit)
				}
				if strings.HasPrefix(path, "injected") {
					<-ctx.Done()
					return -1, nil
				}
				return 0, nil
			}}
			var err error
			switch path {
			case "host":
				_, err = runContainerCheck(context.Background(), tree, "build", ".", runner, copies, grantContainerBudget())
			case "summary":
				_, err = runContainerSummary(context.Background(), tree, "build", ".", runner, copies, containerTimeout, grantContainerBudget())
			case "production verifier", "injected verifier":
				v := newContainerVerifier(tree)
				v.runner, v.copyParent, v.budget = runner, copies, grantContainerBudget()
				if path == "injected verifier" {
					v.timeout = limit
				}
				_, err = v.verify(context.Background(), "build", ".")
			default:
				var execution containerExecution
				if path == "injected execution" {
					execution.timeout = limit
				}
				_, err = runContainerExecution(context.Background(), tree, "build", ".", runner, copies, grantContainerExecution(&execution))
			}
			if !called || strings.HasPrefix(path, "injected") != errors.Is(err, context.DeadlineExceeded) || limit == containerTimeout && err != nil {
				t.Fatalf("called=%v, error=%v", called, err)
			}
		})
	}
}

func TestContainerTimeoutRows(t *testing.T) {
	for _, row := range []deadlineRow{
		{"row 1 deadline while running", "run", "interrupted", context.DeadlineExceeded},
		{"row 2 deadline and output cap", "run", "cap", context.DeadlineExceeded},
		{"row 3 deadline after pass stop", "stop", "pass", context.DeadlineExceeded},
		{"row 3 deadline after pass rm", "rm", "pass", context.DeadlineExceeded},
		{"row 3 deadline after pass finishReport", "finishReport", "pass", context.DeadlineExceeded},
		{"row 4 deadline after failure", "rm", "fail", context.DeadlineExceeded},
	} {
		for _, check := range verifyCheckNames() {
			t.Run(row.name+"/"+check, func(t *testing.T) {
				t.Parallel()
				characterizeDeadlineRow(t, check, row, true)
			})
		}
	}
}

func TestContainerContextNeverVerified(t *testing.T) {
	for _, interruption := range []error{context.DeadlineExceeded, context.Canceled} {
		for _, stage := range []string{"before", "run", "stop", "rm", "finishReport"} {
			for _, outcome := range []string{"pass", "fail"} {
				for _, check := range verifyCheckNames() {
					t.Run(interruption.Error()+"/"+stage+"/"+outcome+"/"+check, func(t *testing.T) {
						t.Parallel()
						// The shared row assertion checks the complete host report,
						// summary, real verifyResult, isError and fixed minimal body.
						row := deadlineRow{"never verified", stage, outcome, interruption}
						characterizeDeadlineRow(t, check, row, true)
					})
				}
			}
		}
	}
}

func TestContainerRunnerErrorLateDeadline(t *testing.T) {
	for _, stage := range []string{"stop", "rm"} {
		for _, check := range verifyCheckNames() {
			t.Run(stage+"/"+check, func(t *testing.T) {
				t.Parallel()
				row := deadlineRow{"row 8 runner error then late deadline", stage, "runner error", context.DeadlineExceeded}
				characterizeDeadlineRow(t, check, row, true)
			})
		}
	}
}

func TestContainerStatus125LateDeadline(t *testing.T) {
	for _, stage := range []string{"stop", "rm"} {
		for _, check := range verifyCheckNames() {
			t.Run(stage+"/"+check, func(t *testing.T) {
				t.Parallel()
				// Pin row 8's entire late-deadline host report: a Docker failure
				// has no generated stat, and its MCP body stays incomplete.
				row := deadlineRow{"row 8 status 125 then late deadline", stage, "125", context.DeadlineExceeded}
				characterizeDeadlineRow(t, check, row, true)
			})
		}
	}
}

func TestContainerLateDeadlineGeneratedOutput(t *testing.T) {
	for _, stage := range []string{"stop", "rm"} {
		for _, tc := range []struct {
			name, stdout, verdict, stat, output string
			code                                int
		}{
			{"diff", generatedFixture("a.go", "@@ -1 +1 @@\n-a\n+b\n"), "FAILED: generated files differ", "a.go +1 -1\n1 paths, +1 -1\n", "", generatedDiffer},
			{"gofmt", "a.go\nb.go\n", "FAILED: formatting", "", "a.go\nb.go", generatedGofmtNames},
			{"status", "", "FAILED", "Generated check could not complete (status 24).\n", "\n", generatedGofmtFailed},
		} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				tree, copies := containerFixture(t), t.TempDir()
				var activeRun context.Context
				base := summaryRunner(tc.stdout, "", tc.code)
				runner := fakeContainerRunner{func(ctx context.Context, args, env []string, out, diagnostic io.Writer) (int, error) {
					if args[0] == "run" {
						activeRun = ctx
						if ctx.Err() != nil {
							t.Fatal("deadline expired before run")
						}
					}
					if args[0] == stage {
						<-activeRun.Done()
					}
					return base.run(ctx, args, env, out, diagnostic)
				}}
				execution := containerExecution{timeout: 2 * time.Second}
				report, err := runContainerExecution(context.Background(), tree, "generated", "./...", runner, copies, grantContainerExecution(&execution))
				output := tc.output
				if tc.name == "diff" {
					output = tc.stdout + "\n"
				}
				want := tc.verdict + "\n" + tc.stat + containerCheckScope("generated") + "\n--- stdout\n" + output + "\n--- stderr\n\n"
				if report != want || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("late deadline generated report %q, want %q; error %v", report, want, err)
				}
				summary, mcpErr := (containerVerifier{grantContainerBudget(), tree, runner, copies, 2 * time.Second}).verify(context.Background(), "generated", "./...")
				if summary != execution.summary || !errors.Is(mcpErr, context.DeadlineExceeded) {
					t.Fatalf("late deadline host/MCP mismatch: %q, %v", summary, mcpErr)
				}
				assertIncompleteSummary(t, report, summary, mcpErr)
			})
		}
	}
}

func TestContainerContextCommandExit(t *testing.T) {
	for _, check := range verifyCheckNames() {
		for _, stage := range []string{"run", "stop", "rm"} {
			t.Run(check+"/"+stage, func(t *testing.T) {
				t.Parallel()
				var cancel context.CancelFunc
				stdout, _, _ := deadlineFixture(check, "pass")
				runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
					if args[0] == "run" {
						cancel = out.(containerStream).output.cancel
						_, _ = io.WriteString(out, stdout)
					}
					if args[0] == stage {
						cancel()
					}
					return 0, nil
				}}
				var out, diagnostic bytes.Buffer
				code := startContainerCheck([]string{containerFixture(t), check, "./..."}, &out, &diagnostic, nil, func(io.Writer) error { return nil }, runner, t.TempDir(), grantContainerBudget())
				if code == 0 || strings.HasPrefix(out.String(), "verified") || !strings.HasPrefix(out.String(), "FAILED\n") {
					t.Fatalf("command exit %d, report %q, error %s", code, &out, &diagnostic)
				}
			})
		}
	}
}

func TestContainerDeadlineRemovalFailure(t *testing.T) {
	for _, check := range verifyCheckNames() {
		t.Run(check, func(t *testing.T) {
			t.Parallel()
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, _, _ io.Writer) (int, error) {
				if args[0] == "run" {
					<-ctx.Done()
					return -1, nil
				}
				if args[0] == "rm" {
					return 1, errors.New("removal unconfirmed")
				}
				return 0, nil
			}}
			tree, copies := containerFixture(t), t.TempDir()
			execution := containerExecution{timeout: 2 * time.Second}
			report, err := runContainerExecution(context.Background(), tree, check, "./...", runner, copies, grantContainerExecution(&execution))
			if !strings.HasPrefix(report, "FAILED\n") || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "container removal failed") {
				t.Fatalf("deadline with unconfirmed removal: %q, %v", report, err)
			}
			assertIncompleteSummary(t, report, execution.summary, err)
			summary, mcpErr := (containerVerifier{grantContainerBudget(), tree, runner, copies, 2 * time.Second}).verify(context.Background(), check, "./...")
			if summary != execution.summary || !errors.Is(mcpErr, context.DeadlineExceeded) {
				t.Fatalf("removal failure host/MCP mismatch: %q, %v", summary, mcpErr)
			}
			assertIncompleteSummary(t, report, summary, mcpErr)
		})
	}
}
