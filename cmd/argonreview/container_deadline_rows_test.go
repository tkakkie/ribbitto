package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type deadlineRow struct {
	name, stage, outcome string
	interruption         error
}

func TestContainerDeadlineRows(t *testing.T) {
	rows := []deadlineRow{
		{"row 2 deadline and output cap", "run", "cap", context.DeadlineExceeded},
		{"row 6 deadline before container", "before", "pass", context.DeadlineExceeded},
		{"row 7 cleanup timeout alone", "rm", "cleanup timeout", nil},
		{"row 8 runner error", "run", "runner error", nil},
		{"row 8 exit status 125", "run", "125", nil},
		{"row 9 cancel before container", "before", "pass", context.Canceled},
		{"row 9 cancel while running", "run", "interrupted", context.Canceled},
		{"row 10 source cleanup failure", "source", "pass", nil},
	}
	for _, stage := range []string{"stop", "rm", "finishReport"} {
		rows = append(rows,
			deadlineRow{"row 4 deadline after failing result " + stage, stage, "fail", context.DeadlineExceeded},
			deadlineRow{"row 4 deadline after malformed stream " + stage, stage, "malformed", context.DeadlineExceeded},
			deadlineRow{"row 5 deadline after skipped result " + stage, stage, "skip", context.DeadlineExceeded},
			deadlineRow{"row 5 deadline after no tests " + stage, stage, "no tests", context.DeadlineExceeded},
			deadlineRow{"row 9 cancel after verified result " + stage, stage, "pass", context.Canceled},
			deadlineRow{"row 9 cancel after failing result " + stage, stage, "fail", context.Canceled},
		)
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			for _, check := range verifyCheckNames() {
				if (row.outcome == "skip" || row.outcome == "no tests" || row.outcome == "malformed") && check != "test" && check != "test-db" {
					continue
				}
				t.Run(check, func(t *testing.T) {
					t.Parallel()
					characterizeDeadlineRow(t, check, row)
				})
			}
		})
	}
}

func deadlineFixture(check, outcome string) (stdout, verdict string, code int) {
	verdict = "verified"
	isTest := check == "test" || check == "test-db"
	if isTest {
		stdout, verdict = containerEvents("p", "TestOne", "pass"), "verified\n1 passing leaf tests"
	}
	switch outcome {
	case "fail":
		verdict, code = "FAILED", 1
		if isTest {
			stdout = containerJSON("p", "start", "run/TestOne", "fail/TestOne", "fail")
		} else if check == "generated" {
			verdict, code = "FAILED: generation failed", generatedGenerationFailed
		} else {
			stdout = "compiler output"
		}
	case "skip":
		stdout, verdict = containerEvents("p", "TestSkip", "skip"), "NOT VERIFIED: 1 skipped\np/TestSkip"
	case "no tests":
		stdout, verdict = containerJSON("p", "start", "pass"), "NOT VERIFIED: a targeted package has no tests, or no leaf test passed"
	case "malformed":
		stdout, verdict = "garbage\n", "FAILED: malformed test stream"
	case "cap":
		stdout = strings.Repeat("x", containerOutputLimit+1)
	case "125":
		verdict, code = "FAILED", 125
	}
	return stdout, verdict, code
}

func characterizeDeadlineRow(t *testing.T, check string, row deadlineRow, injected ...bool) {
	t.Helper()
	tree, copies := containerFixture(t), t.TempDir()
	stdout, original, code := deadlineFixture(check, row.outcome)
	const stderr = "runner diagnostic"
	runnerFailure := errors.New("runner failed")
	var execution containerExecution
	timeout := containerTimeout
	if len(injected) != 0 && injected[0] {
		timeout = 2 * time.Second
	}
	execution.timeout = timeout
	var baselineSummary string
	run := func(mcp bool) (string, string, error) {
		ctx, cancel := context.WithCancel(context.Background())
		if row.interruption == context.DeadlineExceeded && (timeout == containerTimeout || row.stage == "before" || row.stage == "finishReport") {
			cancel()
			// Leave Git preparation ample time; only the selected hook waits
			// for expiry. Independent row cases run in parallel to keep this cheap.
			limit := 2 * time.Second
			if row.stage == "before" || row.stage == "finishReport" {
				limit = -time.Second
			}
			ctx, cancel = context.WithTimeout(context.Background(), limit)
		}
		defer cancel()
		var activeRun context.Context
		interrupt := func() {
			if row.interruption == context.Canceled {
				cancel()
			}
			if timeout == containerTimeout || row.stage == "before" || row.stage == "finishReport" || row.interruption == context.Canceled {
				<-ctx.Done()
			}
			// A parent's Done can close before cancellation reaches its child.
			// The verdict reads the derived run context, so wait for it too.
			if activeRun != nil {
				<-activeRun.Done()
			}
		}
		if row.stage == "before" {
			interrupt()
		}
		var calls []string
		base := summaryRunner(stdout, stderr, code)
		runner := fakeContainerRunner{func(runCtx context.Context, args, env []string, out, diagnostic io.Writer) (int, error) {
			calls = append(calls, args[0])
			if args[0] == "run" {
				activeRun = runCtx
			}
			if row.stage == "before" {
				t.Fatal("runner called before source preparation completed")
			}
			if args[0] == row.stage && row.interruption != nil {
				interrupt()
			}
			if args[0] == "rm" && row.outcome == "cleanup timeout" {
				if ctx.Err() != nil || runCtx.Err() != nil || activeRun.Err() != nil {
					t.Fatal("cleanup-only timeout interrupted the run context")
				}
				return -1, fmt.Errorf("cleanup: %w", context.DeadlineExceeded)
			}
			if args[0] == "run" {
				if row.stage != "run" && row.stage != "finishReport" && runCtx.Err() != nil {
					t.Fatal("parent expired before the requested cleanup stage")
				}
				if row.stage == "source" {
					for _, arg := range args {
						if strings.HasSuffix(arg, ",dst=/work,readonly") {
							dir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
							if err := os.Chmod(dir, 0o500); err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.RemoveAll(filepath.Dir(dir)) })
						}
					}
				}
				if row.outcome == "runner error" {
					_, _ = base.run(runCtx, args, env, out, diagnostic)
					if row.stage != "run" {
						// A passing stream/status cannot prove completion after a runner error.
						return code, runnerFailure
					}
					return -1, runnerFailure
				}
				if row.stage == "run" && (errors.Is(row.interruption, context.DeadlineExceeded) || errors.Is(row.interruption, context.Canceled)) {
					_, _ = base.run(runCtx, args, env, out, diagnostic)
					return -1, nil // Docker's killed-client exit status, without a runner error.
				}
			}
			return base.run(runCtx, args, env, out, diagnostic)
		}}
		runCtx := context.Context(ctx)
		if row.stage == "finishReport" {
			runCtx = context.Background()
		}
		var report, summary string
		var err error
		if mcp {
			summary, err = (containerVerifier{tree, runner, copies, timeout}).verify(runCtx, check, "./...")
		} else {
			report, err = runContainerExecution(runCtx, tree, check, "./...", runner, copies, &execution)
			summary = execution.summary
		}
		wantCalls := []string{"run", "stop", "rm"}
		if row.stage == "before" {
			wantCalls = nil
		}
		if !reflect.DeepEqual(calls, wantCalls) {
			t.Fatalf("runner calls %v, want %v", calls, wantCalls)
		}
		if row.stage == "finishReport" {
			// The last runner hook precedes finishContainerRun's verdict. Source
			// removal has no runner hook, so replay only deferred finalisation.
			if err != nil {
				t.Fatal(err)
			}
			if !mcp {
				baselineSummary = summary
			}
			if row.interruption == context.Canceled {
				cancel()
			}
			if mcp {
				if summary != baselineSummary {
					t.Fatal("verifier baseline differs from captured execution")
				}
				report = deadlineHostReport(check, original, stdout, stderr, code, false)
			}
			in := summaryFixture(check, original, "", "")
			report, err = execution.finishReport(ctx, report, nil, in)
			summary = execution.summary
		}
		return report, summary, err
	}
	report, summary, err := run(false)
	first := "FAILED"
	verdict := first
	if row.interruption == context.DeadlineExceeded && row.outcome != "runner error" && (row.stage == "run" || row.outcome == "pass" && row.stage != "before") {
		// #1857: was FAILED (rows 1, 3 and row 11's parent-deadline path).
		verdict = "NOT VERIFIED: time limit exceeded"
	}
	lateDeadline := row.interruption == context.DeadlineExceeded && (row.stage == "stop" || row.stage == "rm") && row.outcome != "runner error"
	if lateDeadline && row.outcome != "pass" {
		// #1857: was FAILED (rows 4/5 now keep their own completed verdict).
		verdict = original
	}
	if row.stage == "finishReport" && row.outcome != "pass" {
		verdict = original
	} else if row.outcome == "cap" {
		verdict = "NOT VERIFIED: output limit exceeded (output cut off)"
	} else if row.stage == "source" {
		verdict = "FAILED: source cleanup failed"
	} else if row.stage == "finishReport" && (check == "test" || check == "test-db") {
		verdict += "\n1 passing leaf tests"
	}
	first, _, _ = strings.Cut(verdict, "\n")
	// #1857: was FAILED's error-bearing generated output, even after a completed pass.
	wantReport := deadlineHostReport(check, verdict, stdout, stderr, code, row.stage != "finishReport" && !lateDeadline)
	switch row.stage {
	case "before":
		wantReport = ""
	case "source":
		wantReport = fmt.Sprintf("%s\n%s check result could not be confirmed. %s\n", first, check, containerCheckScope(""))
	}
	if report != wantReport {
		t.Fatalf("host report differs: got %q, want %q", report[:min(len(report), 1500)], wantReport[:min(len(wantReport), 1500)])
	}
	wantErr := row.interruption
	if row.outcome == "cleanup timeout" {
		wantErr = context.DeadlineExceeded
	}
	if wantErr != nil && !errors.Is(err, wantErr) || (err == nil) != (row.outcome == "125" && row.interruption == nil) {
		t.Fatalf("host error %v, expected context error %v", err, wantErr)
	}
	for outcome, detail := range map[string]string{"cleanup timeout": "container removal failed", "runner error": "runner failed", "pass": "removing source copy"} {
		if row.outcome == outcome && (outcome != "pass" || row.stage == "source") && !strings.Contains(err.Error(), detail) {
			t.Fatalf("missing error detail %q: %v", detail, err)
		}
	}
	_, mcpSummary, mcpErr := run(true)
	if summary != mcpSummary || (err == nil) != (mcpErr == nil) || wantErr != nil && !errors.Is(mcpErr, wantErr) {
		t.Fatalf("host/MCP mismatch: summaries %q / %q, errors %v / %v", summary, mcpSummary, err, mcpErr)
	}
	if row.outcome == "runner error" && (!errors.Is(err, runnerFailure) || !errors.Is(mcpErr, runnerFailure)) {
		t.Fatalf("runner error lost: host %v, MCP %v", err, mcpErr)
	}
	if row.stage == "before" {
		first = "NOT VERIFIED: cancelled"
		if row.interruption == context.DeadlineExceeded {
			first = "NOT VERIFIED: time limit exceeded"
		}
		if summary != "" {
			t.Fatal("pre-container refusal produced a summary")
		}
	} else {
		scope := containerCheckScope(check)
		if row.stage == "source" {
			scope = containerCheckScope("")
		}
		body := "Details unavailable: the check did not complete.\n"
		if row.outcome == "cap" {
			body = "Counts unavailable: output limit exceeded; the stream was cut.\n"
		}
		want := first + "\nCheck: " + check + " ./...\nScope: " + scope + "\n" + body + deadlineOmitted
		if summary != want {
			t.Fatalf("summary body differs: %q, want %q", summary, want)
		}
		if row.outcome != "cap" {
			assertIncompleteSummary(t, report, summary, err)
		}
	}
	text, bad := verifyResult(mcpSummary, mcpErr, false, false)
	wantText := mcpSummary
	if row.stage == "before" {
		wantText = first + "\n"
	}
	if mcpErr != nil {
		wantText += "\n" + mcpErr.Error()
	}
	if !bad || text != wantText || !strings.HasPrefix(text, first+"\n") {
		t.Fatalf("MCP text %q, isError=%v, want %q", text, bad, wantText)
	}
	// Independent of the expected lines above: no context error, at any stage,
	// may leave the host report, the summary or the MCP text reading verified.
	if row.interruption != nil && (strings.HasPrefix(report, "verified") || strings.HasPrefix(summary, "verified") || strings.HasPrefix(text, "verified")) {
		t.Fatalf("interrupted run read verified: report %q, summary %q, MCP %q", report[:min(len(report), 80)], summary, text)
	}
}

const deadlineOmitted = "Omitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"

func deadlineHostReport(check, verdict, stdout, stderr string, code int, interrupted bool) string {
	stat := ""
	if strings.Contains(verdict, "output limit exceeded") {
		stderr = ""
	}
	if check == "generated" {
		switch {
		case strings.Contains(verdict, "output limit exceeded"):
			stdout = strings.Repeat("x", 394) + " [cut]\n[diff cut off]\n"
		case interrupted:
			stdout += "\n"
			if code == 125 {
				stat = "Generated check could not complete (status 125).\n"
			}
		case code != 0:
			stdout += "\n"
		}
	} else if len(stdout) > containerOutputLimit {
		stdout = stdout[:containerOutputLimit]
	}
	if len(stdout) == containerOutputLimit {
		stderr = "" // The combined budget was exhausted by stdout.
	}
	return fmt.Sprintf("%s\n%s%s\n--- stdout\n%s\n--- stderr\n%s\n", verdict, stat, containerCheckScope(check), stdout, stderr)
}
