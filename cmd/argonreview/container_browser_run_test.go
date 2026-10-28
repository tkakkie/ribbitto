package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func browserRunFixture(t *testing.T) containerVerifier {
	t.Helper()
	b, path, entry := browserGateFixture(t)
	browserWrite(t, b.path, string(entry.bytes()), 0o600)
	return containerVerifier{worktree: containerFixture(t), copyParent: t.TempDir(), browserMode: path, browserSecurity: b, budget: fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
		return budgetReservation{slots: []int{0, 1}, label: "0,1", memory: 2 * budgetUnit, release: func() {}}, ""
	}), runner: summaryRunner(containerEvents(browserTestPackage, browserTestRun, "pass"), "", 0)}
}

func TestContainerBrowserOrder(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		v := browserRunFixture(t)
		// Reaching the gate's clock observes successful validation, switch read and
		// gate read. The refusal rows below isolate each preceding predicate.
		var events []string
		now := v.browserSecurity.now()
		v.browserSecurity.now = func() time.Time { events = append(events, "validate", "switch", "gate"); return now }
		released, removed := false, false
		var copyLock *os.File
		v.budget = fakeContainerBudget(func(_ context.Context, request budgetRequest) (budgetReservation, budgetResult) {
			if !reflect.DeepEqual(events, []string{"validate", "switch", "gate"}) {
				t.Fatalf("admission must precede reservation: %v", events)
			}
			assertNoContainerCopy(t, v.copyParent)
			if request != budgetBrowserRequest {
				t.Fatalf("browser must request browser.lock and two units: %v", request)
			}
			events = append(events, "reserve")
			return budgetReservation{label: "1,2", memory: 2 * budgetUnit, release: func() {
				if !removed || released {
					t.Error("browser release must follow confirmed cleanup")
				}
				assertNoContainerCopy(t, v.copyParent)
				if copyLock != nil {
					if err := unix.Flock(int(copyLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
						t.Errorf("browser copy lock must close before release: %v", err)
					}
				}
				events = append(events, "copy removed", "release")
				released = true
			}}, ""
		})
		v.runner = fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
			if released {
				t.Error("browser released before Docker cleanup")
			}
			if args[0] == "run" {
				dirs, err := os.ReadDir(v.copyParent)
				if err != nil || len(dirs) != 1 {
					t.Fatalf("copy must exist before run: %v %v", dirs, err)
				}
				copyLock, err = os.Open(filepath.Join(v.copyParent, dirs[0].Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := unix.Flock(int(copyLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
					t.Fatal("browser copy lock not held during run")
				}
				events = append(events, "copy dir created", "docker run")
				if !slices.Contains(args, "argonreview.slot=1,2") {
					t.Fatal("browser label must come from reservation")
				}
				_, _ = io.WriteString(stdout, containerEvents(browserTestPackage, browserTestRun, "pass"))
			} else {
				events = append(events, args[0])
				if args[0] == "rm" {
					removed = true
				}
			}
			return 0, nil
		}}
		summary, err := v.verifyBrowser(context.Background(), "./internal/web", browserTestRun)
		if copyLock != nil {
			_ = copyLock.Close()
		}
		want := []string{"validate", "switch", "gate", "reserve", "copy dir created", "docker run", "stop", "rm", "copy removed", "release"}
		if err != nil || !strings.HasPrefix(summary, "verified\n") || !reflect.DeepEqual(events, want) {
			t.Fatalf("browser lifecycle order: events=%v summary=%q err=%v", events, summary, err)
		}
	})
	for _, kind := range []string{"switch-off", "switch-missing", "gate-expired", "gate-missing", "gate-path-differs", "invalid-run", "wrong-package", "busy", "unavailable", "paused", "nil-admission"} {
		t.Run(kind, func(t *testing.T) {
			v := browserRunFixture(t)
			run, pattern := browserTestRun, "./internal/web"
			want := browserPaused
			reserved, calls, gateReads := 0, 0, 0
			now := v.browserSecurity.now()
			v.browserSecurity.now = func() time.Time { gateReads++; return now }
			switch kind {
			case "switch-off":
				browserWrite(t, v.browserMode, "off", 0o400)
			case "switch-missing":
				browserRemove(t, v.browserMode)
			case "gate-expired":
				v.browserSecurity.now = func() time.Time { gateReads++; return now.Add(browserSecurityAge) }
				want = browserOverdue
			case "gate-missing":
				browserRemove(t, v.browserSecurity.path)
				want = browserOverdue
			case "gate-path-differs":
				browserWrite(t, v.browserSecurity.path, string((browserSecurityEntry{v.browserMode + "-other", now.Add(-time.Hour), "https://github.com/tkakkie/ribbitto/issues/1908"}).bytes()), 0o600)
				want = browserOverdue
			case "invalid-run":
				run = "TestBrowser"
				want = ""
			case "wrong-package":
				pattern = "./..."
				want = ""
			case "busy":
				want = "NOT VERIFIED: browser check busy"
			case "unavailable":
				want = budgetStateUnknown.line("verification budget")
			case "paused":
				want = budgetResult("paused: an exclusive container is present").line("verification budget")
			}
			v.budget = fakeContainerBudget(func(_ context.Context, r budgetRequest) (budgetReservation, budgetResult) {
				reserved++
				assertNoContainerCopy(t, v.copyParent)
				if r != budgetBrowserRequest {
					t.Fatal("refused browser request must still use browser units")
				}
				if kind == "busy" {
					return budgetReservation{}, budgetBusy
				}
				if kind == "paused" {
					return budgetReservation{}, budgetResult("paused: an exclusive container is present")
				}
				return budgetReservation{}, budgetStateUnknown
			})
			v.runner = fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { calls++; return 0, nil }}
			var summary string
			var err error
			if kind == "nil-admission" {
				execution := containerExecution{budget: v.budget, browserRun: run, browserImportPath: browserTestPackage}
				_, err = runContainerExecution(context.Background(), v.worktree, "test-browser", pattern, v.runner, v.copyParent, &execution)
				summary = execution.summary
				if execution.code != 1 {
					t.Fatal("nil admission must fail closed with code 1")
				}
			} else {
				summary, err = v.verifyBrowser(context.Background(), pattern, run)
			}
			expectedReservations := 0
			if kind == "busy" || kind == "unavailable" || kind == "paused" {
				expectedReservations = 1
			}
			if reserved != expectedReservations || calls != 0 {
				t.Fatalf("browser refusal before reservation/copy/Docker: reserve=%d want=%d Docker=%d", reserved, expectedReservations, calls)
			}
			assertNoContainerCopy(t, v.copyParent)
			if want == "" {
				if err == nil || summary != "" || gateReads != 0 {
					t.Fatalf("validation must precede admission: summary=%q err=%v gate=%d", summary, err, gateReads)
				}
			} else if err != nil || !strings.HasPrefix(summary, want+"\n") {
				t.Fatalf("browser refusal line: %q want %q err=%v", summary, want, err)
			}
		})
	}
	t.Run("per-call-files", func(t *testing.T) {
		v := browserRunFixture(t)
		for _, kind := range []string{"on", "off", "on", "expired"} {
			want := "verified\n"
			if kind == "expired" {
				v.browserSecurity.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
				want = browserOverdue
			} else {
				browserWrite(t, v.browserMode, kind, 0o400)
				if kind == "off" {
					want = browserPaused
				}
			}
			summary, err := v.verifyBrowser(context.Background(), "./internal/web", browserTestRun)
			if err != nil || !strings.HasPrefix(summary, want) {
				t.Fatalf("per-call admission %s: %q %v", kind, summary, err)
			}
		}
	})
}

func TestContainerBrowserArguments(t *testing.T) {
	for _, check := range []string{"build", "vet", "test", "generated", "test-db", "writeback", "test-browser"} {
		for _, predicate := range []string{"class", "pids", "browser-env", "db-env", "positional-run", "fixed-script", "no-other-gates", "reservation-memory"} {
			t.Run(check+"/"+predicate, func(t *testing.T) {
				reservation := budgetReservation{label: "1,2", memory: 2 * budgetUnit}
				args := containerArguments("copy", "name", check, "./internal/web", reservation)
				browser := check == "test-browser"
				if browser {
					args = containerBrowserArguments("copy", "name", "./internal/web", browserTestRun, reservation)
				}
				has := func(s string) bool { return slices.Contains(args, s) }
				value := func(key string) string {
					i := slices.Index(args, key)
					if i < 0 || i+1 >= len(args) {
						return ""
					}
					return args[i+1]
				}
				switch predicate {
				case "class":
					if has("argonreview.class=browser") != browser {
						t.Fatal("class label must be browser-only")
					}
				case "pids":
					want := "256"
					if browser {
						want = "2048"
					}
					if value("--pids-limit") != want {
						t.Fatalf("pids must be %s", want)
					}
				case "browser-env":
					if has("RIBBITTO_REQUIRE_BROWSER=1") != browser {
						t.Fatal("browser requirement must be browser-only")
					}
				case "db-env":
					for _, env := range []string{containerTestDatabaseURL, containerRequireDB} {
						if has(env) != (browser || check == "test-db") {
							t.Fatal("DB environment must be present only for DB/browser")
						}
					}
				case "positional-run":
					if browser && !reflect.DeepEqual(args[len(args)-3:], []string{"sh", "./internal/web", "^TestBrowser$/^quote$"}) {
						t.Fatal("anchored run must be positional after package")
					}
				case "fixed-script":
					if browser {
						want := strings.Replace(testDBScriptGolden, `go test -count=1 -json -p 2 "$1"`, `go test -count=1 -json -p 2 -run "$2" "$1"`, 1)
						if value("-c") != want || strings.Contains(value("-c"), browserTestRun) {
							t.Fatal("browser script must use positional -run, never interpolated run")
						}
					}
					if containerTestDBScript != testDBScriptGolden {
						t.Fatal("test-db script changed")
					}
				case "no-other-gates":
					for i, arg := range args {
						if arg == "-e" {
							env := args[i+1]
							key, _, _ := strings.Cut(env, "=")
							if key == "CI" || strings.HasPrefix(key, "RIBBITTO_") && env != containerTestDatabaseURL && env != containerRequireDB && env != containerRequireBrowser {
								t.Fatalf("forbidden browser environment gate: %s", key)
							}
						}
					}
				case "reservation-memory":
					for _, units := range []int64{1, 2, 3} {
						reservation.memory = units * budgetUnit
						if browser {
							args = containerBrowserArguments("copy", "name", "./internal/web", browserTestRun, reservation)
						} else {
							args = containerArguments("copy", "name", check, "./internal/web", reservation)
						}
						for _, key := range []string{"--memory", "--memory-swap"} {
							if value(key) != fmt.Sprintf("%dg", 2*units) {
								t.Fatal("memory must come from reservation")
							}
						}
					}
				}
			})
		}
	}
}
