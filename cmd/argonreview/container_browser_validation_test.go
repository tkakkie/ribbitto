package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestContainerBrowserCommonPathValidation(t *testing.T) {
	for _, row := range []struct {
		name, pattern, run string
		valid              bool
	}{
		{"empty-run", "./internal/web", "", false},
		{"unnamed-run", "./internal/web", "TestBrowser", false},
		{"regexp-wildcard", "./internal/web", "TestBrowser/.*", false},
		{"regexp-alternative", "./internal/web", "TestBrowser/a|b", false},
		{"regexp-anchors", "./internal/web", "^TestBrowser$", false},
		{"flag", "./internal/web", "-test.v", false},
		{"space", "./internal/web", "TestBrowser/a b", false},
		{"nul", "./internal/web", "TestBrowser/a\x00", false},
		{"201-bytes", "./internal/web", strings.Repeat("a", 201), false},
		{"all-packages", "./...", browserTestRun, false},
		{"other-package", "./internal/org", browserTestRun, false},
		{"recursive-package", "./internal/web/...", browserTestRun, false},
		{"empty-package", "", browserTestRun, false},
		{"valid", "./internal/web", browserTestRun, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			tree, parent := containerFixture(t), t.TempDir()
			admissions, reservations, releases := 0, 0, 0
			var calls [][]string
			var runArguments []string
			result := containerExecution{browserRun: row.run, admit: func() string {
				admissions++
				return ""
			}, budget: fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
				reservations++
				return budgetReservation{label: "0,1", memory: 2 * budgetUnit, release: func() { releases++ }}, ""
			})}
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
				calls = append(calls, slices.Clone(args))
				if args[0] == "run" {
					runArguments = slices.Clone(args)
					_, _ = io.WriteString(stdout, containerEvents(browserTestPackage, browserTestRun, "pass"))
				}
				return 0, nil
			}}
			report, err := runContainerExecution(context.Background(), tree, "test-browser", row.pattern, runner, parent, &result)
			assertNoContainerCopy(t, parent)
			if row.valid {
				if err != nil || !strings.HasPrefix(report, "verified\n") || !strings.HasPrefix(result.summary, "verified\n") {
					t.Fatalf("valid-common-path: report=%q summary=%q error=%v", report, result.summary, err)
				}
				if admissions != 3 || reservations != 1 || releases != 1 || len(calls) != 3 {
					t.Fatalf("valid-lifecycle: admission=%d budget=%d release=%d calls=%v", admissions, reservations, releases, calls)
				}
				want, err := browserRunPattern(row.run)
				if err != nil {
					t.Fatal(err)
				}
				if len(runArguments) < 3 || !slices.Equal(runArguments[len(runArguments)-3:], []string{"sh", row.pattern, want}) {
					t.Fatalf("valid-run-argument: args=%v want run=%q", runArguments, want)
				}
				return
			}
			if err == nil || report != "" || result.summary != "" {
				t.Errorf("validation-error-without-report: error=%v report=%q summary=%q", err, report, result.summary)
			}
			if admissions != 0 {
				t.Errorf("validation-before-admission: calls=%d", admissions)
			}
			if reservations != 0 || releases != 0 {
				t.Errorf("validation-before-budget: reserve=%d release=%d", reservations, releases)
			}
			if len(calls) != 0 {
				t.Errorf("validation-before-runner: calls=%v", calls)
			}
			if len(runArguments) != 0 {
				t.Errorf("no-run-arguments-built: args=%v", runArguments)
			}
		})
	}
}

func TestContainerBrowserArgumentsRejectInvalidRun(t *testing.T) {
	_, refused := browserRunCases()
	for _, run := range refused {
		t.Run(run, func(t *testing.T) {
			args, err := containerBrowserArguments("copy", "name", "./internal/web", run, budgetReservation{label: "0,1", memory: 2 * budgetUnit})
			if err == nil || len(args) != 0 {
				t.Fatalf("invalid-run-builds-no-arguments: args=%v error=%v", args, err)
			}
		})
	}
}

func TestContainerBrowserArgumentsFailureReleases(t *testing.T) {
	tree, parent := containerFixture(t), t.TempDir()
	calls, reservations, releases := 0, 0, 0
	result := containerExecution{browserRun: browserTestRun}
	// Simulate a changed run after common validation to exercise the argument
	// builder's defence and the executor's already-registered cleanup path.
	result.admit = func() string { result.browserRun = "TestBrowser"; return "" }
	result.budget = fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
		reservations++
		return budgetReservation{label: "0,1", memory: 2 * budgetUnit, release: func() {
			assertNoContainerCopy(t, parent)
			releases++
		}}, ""
	})
	runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		calls++
		return 0, nil
	}}
	report, err := runContainerExecution(context.Background(), tree, "test-browser", "./internal/web", runner, parent, &result)
	if err == nil || report != "" || result.summary != "" {
		t.Errorf("argument-failure-returns-error: error=%v report=%q summary=%q", err, report, result.summary)
	}
	if reservations != 1 || releases != 1 {
		t.Errorf("argument-failure-releases-budget: reserve=%d release=%d", reservations, releases)
	}
	if calls != 0 {
		t.Errorf("argument-failure-before-docker: calls=%d", calls)
	}
	assertNoContainerCopy(t, parent)
}
