package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestContainerBrowserVerdict(t *testing.T) {
	// Reuse the pure verdict corpus, now through copying, execution, cleanup and
	// summary wiring. Invalid calls belong to validation and never reach Docker.
	for _, row := range browserVerdictCases() {
		if validateBrowserRun(row.run) != nil {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			v := browserRunFixture(t)
			v.runner = fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
				if args[0] != "run" {
					return 0, nil
				}
				if row.truncated {
					_, _ = io.WriteString(stdout, strings.Repeat("x", containerOutputLimit+1))
				} else {
					_, _ = io.WriteString(stdout, row.data)
				}
				return row.code, row.err
			}}
			summary, _ := v.verifyBrowser(context.Background(), "./internal/web", row.run)
			first, _, _ := strings.Cut(row.want, "\n")
			if row.truncated {
				first = "NOT VERIFIED: output limit exceeded (output cut off)"
			}
			if !strings.HasPrefix(summary, first+"\n") {
				t.Fatalf("browser execution verdict: %q want %q", summary, first)
			}
			if first == "verified" && !strings.Contains(summary, row.want+"\n") {
				// Metadata may be between the first and named-test lines.
				_, named, _ := strings.Cut(row.want, "\n")
				if !strings.Contains(summary, named+"\n") {
					t.Fatalf("browser summary must retain named-test verdict: %q", summary)
				}
			}
		})
	}
	t.Run("deadline-after-pass", func(t *testing.T) {
		v := browserRunFixture(t)
		// Use the real deadline context, expiring during cleanup after a pass.
		runDone := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		v.runner = fakeContainerRunner{func(runCtx context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
			if args[0] == "run" {
				_, _ = io.WriteString(stdout, containerEvents(browserTestPackage, browserTestRun, "pass"))
				close(runDone)
			}
			if args[0] == "stop" {
				<-runDone
				<-ctx.Done()
			}
			return 0, runCtx.Err()
		}}
		summary, err := v.verifyBrowser(ctx, "./internal/web", browserTestRun)
		if err == nil || !strings.HasPrefix(summary, containerTimeLimitVerdict+"\n") {
			t.Fatalf("browser pass after deadline must not verify: %q %v", summary, err)
		}
	})
}

func TestContainerBrowserSummaryLimit(t *testing.T) {
	run := "TestBrowser/" + strings.Repeat("a", 62) + "/" + strings.Repeat("b", 62) + "/" + strings.Repeat("c", 62)
	if len(run) != 200 {
		t.Fatalf("fixture run length = %d", len(run))
	}
	for _, code := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit-%d", code), func(t *testing.T) {
			v := browserRunFixture(t)
			v.runner = summaryRunner(containerEvents(browserTestPackage, run, "pass"), strings.Repeat("stderr\n", 10000), code)
			summary, err := v.verifyBrowser(context.Background(), "./internal/web", run)
			limit := summaryErrorBudget
			if code == 0 {
				limit = summaryVerifiedLimit
				if !strings.HasPrefix(summary, "verified\n") || !strings.Contains(summary, "named test: "+run+" passed (1 leaf tests)\n") {
					t.Fatalf("verified browser summary must retain named-test line: %q", summary)
				}
				for _, counts := range []string{"Packages: 1 passed, 0 failed, 0 skipped\n", "Leaf tests: 1 passed, 0 failed, 0 skipped\n"} {
					if !strings.Contains(summary, counts) {
						t.Fatalf("verified browser summary must retain counts: missing=%q summary=%q", counts, summary)
					}
				}
			}
			if err != nil || len(summary) > limit {
				t.Fatalf("browser summary exceeds byte budget: got=%d limit=%d err=%v", len(summary), limit, err)
			}
			if !strings.Contains(summary, "Check: test-browser ./internal/web "+run+"\n") {
				t.Fatal("browser Check line must show package and complete run")
			}
		})
	}
}

func TestContainerBrowserScope(t *testing.T) {
	scope := containerCheckScope("test-browser")
	for _, phrase := range []string{containerChromiumVersion, "(Debian)", "Sandbox disabled", "container is the only boundary", "Not CI's Chrome", "not a browser-targets record", "Only the named test ran", "No race detector"} {
		t.Run(phrase, func(t *testing.T) {
			if !strings.Contains(scope, phrase) {
				t.Fatalf("browser scope missing %q", phrase)
			}
		})
	}
	if len(scope) > 512 {
		t.Fatalf("browser scope exceeds 512 bytes: %d", len(scope))
	}
	for check, want := range map[string]string{
		"test-db":   "Race detector was not run. Cgo-dependent packages and browser tests are CI-only. Ran on regenerated sources with PostgreSQL 18 and the trusted measurement-skip allowlist; the generated check covers committed generated files. test-db is merged but not in force until #1700. Whether ./... fits the unchanged container limits is unestablished.",
		"generated": "Checks templ, sqlc and Tailwind output against committed files, plus templ fmt and gofmt, in a copy. Report only: no write-back; nothing written to the worktree. tbls docs/schema, docs/dependencies.md, the API manifest, golangci-lint, docscheck, languagecheck, race detector, database and browser tests stay CI-only.",
		"build":     "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only. Ran on regenerated sources (templ, sqlc, Tailwind); the generated check covers whether the committed generated files are current.",
		"vet":       "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only. Ran on regenerated sources (templ, sqlc, Tailwind); the generated check covers whether the committed generated files are current.",
		"test":      "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only. Ran on regenerated sources (templ, sqlc, Tailwind); the generated check covers whether the committed generated files are current.",
		"":          "Race detector was not run. Cgo-dependent packages and database/browser tests are CI-only.",
	} {
		if got := containerCheckScope(check); got != want {
			t.Fatalf("existing %s scope changed: %q", check, got)
		}
	}
}

func TestContainerBrowserFailureSummary(t *testing.T) {
	longRun := "TestBrowser/" + strings.Repeat("a", 62) + "/" + strings.Repeat("b", 62) + "/" + strings.Repeat("c", 62)
	for _, run := range []string{browserTestRun, longRun} {
		t.Run(run, func(t *testing.T) {
			v := browserRunFixture(t)
			v.runner = summaryRunner(containerJSON(browserTestPackage, "start", "run/"+run, "fail/"+run, "fail"), strings.Repeat("stderr\n", 10000), 1)
			summary, err := v.verifyBrowser(context.Background(), "./internal/web", run)
			name := browserTestPackage + "/" + run
			if run == longRun {
				name = browserTestPackage + "/TestBrowser/" + strings.Repeat("a", 62)
			}
			for _, text := range []string{"FAILED\n", "Packages: 0 passed, 1 failed, 0 skipped", "Leaf tests: 0 passed, 1 failed, 0 skipped", "Failing leaf tests:", name} {
				if err != nil || !strings.Contains(summary, text) {
					t.Fatalf("failed browser summary must retain counts and failing name: missing=%q summary=%q err=%v", text, summary, err)
				}
			}
			if len(summary) > 2048 {
				t.Fatalf("failed browser summary must keep 2048-byte budget with 200-byte run: %d", len(summary))
			}
		})
	}
}
