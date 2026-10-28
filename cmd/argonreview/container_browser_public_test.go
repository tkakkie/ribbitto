package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestContainerBrowserPublicArguments(t *testing.T) {
	base := []string{"/tree", "test-browser", "./internal/web", "TestBrowser/quote"}
	for _, row := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"valid", base, true},
		{"valid-mode", append(append([]string{}, base...), "--browser-mode", "/trusted/browser-mode"), true},
		{"missing-run", base[:3], false},
		{"extra", append(append([]string{}, base...), "extra"), false},
		{"bad-package", []string{"/tree", "test-browser", "./...", "TestBrowser/quote"}, false},
		{"bad-run", []string{"/tree", "test-browser", "./internal/web", "TestBrowser"}, false},
		{"ordinary", []string{"/tree", "test", "./..."}, true},
		{"ordinary-mode", []string{"/tree", "test", "./...", "--browser-mode", "/trusted/browser-mode"}, false},
		{"ordinary-run", []string{"/tree", "test", "./...", "TestBrowser/quote"}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			a, mode, err := parseContainerCheck(row.args)
			if (err == nil) != row.valid {
				t.Fatalf("container public shape %s: %v", row.name, err)
			}
			if row.valid {
				if strings.HasPrefix(row.name, "valid") && (a.run != base[3] || a.pattern != base[2]) {
					t.Fatal("container browser arguments must retain package and run")
				}
				if strings.HasSuffix(row.name, "mode") && mode != "/trusted/browser-mode" {
					t.Fatal("container must retain browser-mode")
				}
				return
			}
			var log bytes.Buffer
			if code := containerCheckCommand(row.args, io.Discard, &log); code != 2 || !strings.Contains(log.String(), "usage:") {
				t.Fatalf("container invalid shape must give usage exit 2: %s code=%d log=%s", row.name, code, &log)
			}
		})
	}
}
func TestContainerBrowserNoModePaused(t *testing.T) {
	var out, log bytes.Buffer
	called := false
	runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		called = true
		return 0, nil
	}}
	copies := t.TempDir()
	sweep := func(io.Writer) error { called = true; return nil }
	code := startContainerCheck([]string{"/unused", "test-browser", "./internal/web", browserTestRun}, &out, &log, nil, sweep, runner, copies, grantContainerBudget())
	if code != 1 || out.String() != browserPaused+"\n" || called {
		t.Fatalf("container without browser-mode must pause before sweep/copy/runner: exit=%d out=%q called=%v log=%s", code, &out, called, &log)
	}
	assertNoContainerCopy(t, copies)
	out.Reset()
	log.Reset()
	if code := containerCheckCommand([]string{"/unused", "test-browser", "./internal/web", browserTestRun}, &out, &log); code != 1 || out.String() != browserPaused+"\n" {
		t.Fatalf("container command without browser-mode must pause: exit=%d out=%q log=%s", code, &out, &log)
	}
}
func TestContainerBrowserRecheckAfterWait(t *testing.T) {
	for _, kind := range []string{"switch-off", "gate-expired"} {
		t.Run(kind, func(t *testing.T) {
			v := browserRunFixture(t)
			released, called := false, false
			want := browserPaused
			now := v.browserSecurity.now()
			v.budget = fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
				assertNoContainerCopy(t, v.copyParent)
				if kind == "switch-off" {
					browserWrite(t, v.browserMode, "off", 0o400)
				} else {
					v.browserSecurity.now = func() time.Time { return now.Add(browserSecurityAge) }
				}
				return budgetReservation{label: "0,1", memory: 2 * budgetUnit, release: func() { released = true }}, ""
			})
			// Capture admission through the original variable so the injected clock can change during reservation.
			result := containerExecution{budget: v.budget, browserRun: browserTestRun, admit: func() string { return v.browserAdmission() }}
			v.runner = fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				called = true
				return 0, nil
			}}
			if kind == "gate-expired" {
				want = browserOverdue
			}
			report, err := runContainerExecution(context.Background(), v.worktree, verifyBrowserCheck, "./internal/web", v.runner, v.copyParent, &result)
			if err != nil || !strings.HasPrefix(report, want+"\n") || !released || called {
				t.Fatalf("admission after wait must refuse before copy and runner and release: %s report=%q release=%v runner=%v err=%v", kind, report, released, called, err)
			}
			assertNoContainerCopy(t, v.copyParent)
		})
	}
}

func TestContainerBrowserPublicExecution(t *testing.T) {
	for _, row := range []struct {
		name, action, prefix string
		exit, code           int
	}{
		{"pass", "pass", "verified\n", 0, 0},
		{"failed", "fail", "FAILED\n", 1, 1},
		{"skipped", "skip", "NOT VERIFIED:", 0, 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			v := browserRunFixture(t)
			v.runner = summaryRunner(containerJSON(browserTestPackage, "start", "run/"+browserTestRun, row.action+"/"+browserTestRun, row.action), "", row.exit)
			args := []string{v.worktree, "test-browser", "./internal/web", browserTestRun, "--browser-mode", v.browserMode}
			var out, log bytes.Buffer
			swept := false
			code := v.containerCheck(args, &out, &log, nil, func(io.Writer) error { swept = true; return nil })
			if code != row.code || !swept || !strings.HasPrefix(out.String(), row.prefix) {
				t.Fatalf("container browser execution exit must follow report: %s code=%d out=%q log=%s sweep=%v", row.name, code, &out, &log, swept)
			}
			assertNoContainerCopy(t, v.copyParent)
		})
	}
}
