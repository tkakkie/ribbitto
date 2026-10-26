package main

import (
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
						summary, err := (containerVerifier{tree, runner, parent}).verify(ctx, check, "./...")
						return "", summary, err
					}
					var execution containerExecution
					report, err := runContainerExecution(ctx, tree, check, "./...", runner, parent, &execution)
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
				report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", summaryRunner(tc.stream, "/host/private/worktree", 0), t.TempDir(), &execution)
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
	v := containerVerifier{containerFixture(t), summaryRunner(raw, "", generatedDiffer), t.TempDir()}
	report, hostErr := runContainerCheck(context.Background(), v.worktree, "generated", "./...", v.runner, v.copyParent)
	summary, err := v.verify(context.Background(), "generated", "./...")
	text, bad := verifyResult(summary, err, false, false)
	if hostErr != nil || err != nil || !bad || !strings.HasPrefix(report, "FAILED: generated files differ\nDiff stat: diff was not understood\n") || !strings.Contains(report, raw) || !strings.HasPrefix(text, strings.Split(report, "\n")[0]+"\n") {
		t.Fatalf("generated verdict or host report changed: %q, MCP %q, errors %v/%v", report, text, hostErr, err)
	}
	if !strings.Contains(text, "\nDiff stat: diff was not understood.\n") || !strings.Contains(text, "; 2 lines not shown;") || strings.Contains(text, "malformed diff content") || strings.Contains(text, "unvalidated addition") || strings.Contains(text, "\nDiff:\n") || strings.Contains(text, "Generated:") {
		t.Fatalf("malformed diff was not diagnosed and omitted: %s", text)
	}
}
