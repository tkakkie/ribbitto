package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

type fakeContainerBudget func(context.Context, budgetRequest) (budgetReservation, budgetResult)

func (f fakeContainerBudget) budgetReserve(ctx context.Context, request budgetRequest) (budgetReservation, budgetResult) {
	return f(ctx, request)
}
func singleContainerReservation() budgetReservation {
	return budgetReservation{slots: []int{0}, label: "0", memory: budgetUnit, release: func() {}}
}
func grantContainerBudget() containerBudget {
	return fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
		return singleContainerReservation(), ""
	})
}
func grantContainerExecution(result *containerExecution) *containerExecution {
	result.budget = grantContainerBudget()
	return result
}
func assertNoContainerCopy(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("copy directory exists: %v, %v", entries, err)
	}
}

func TestContainerBudgetOrder(t *testing.T) {
	for _, check := range []string{"build", "vet", "test", "generated", "test-db", "writeback"} {
		for _, outcome := range []string{"pass", "fail", "removal fails", "interrupted"} {
			t.Run(check+"/"+outcome, func(t *testing.T) {
				tree, parent := containerFixture(t), t.TempDir()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var events []string
				released, removed := false, false
				copyDir := ""
				var copyLock *os.File
				budget := fakeContainerBudget(func(ctx context.Context, request budgetRequest) (budgetReservation, budgetResult) {
					assertNoContainerCopy(t, parent)
					if request != budgetSingle {
						t.Fatalf("request %v, want one unit", request)
					}
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > containerTimeout {
						t.Fatal("budget outside run deadline")
					}
					events = append(events, "units taken")
					r := budgetReservation{slots: []int{2}, label: "2", memory: budgetUnit}
					r.release = func() {
						if released || !removed {
							t.Error("release before removal result or duplicate release")
						}
						assertNoContainerCopy(t, parent)
						// The copy's liveness lock must close before the budget is released.
						if copyDir != "" {
							if err := unix.Flock(int(copyLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
								t.Errorf("copy lock still held at release: %v", err)
							}
						}
						events = append(events, "copy removed", "release")
						released = true
					}
					return r, ""
				})
				runner := fakeContainerRunner{func(runCtx context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
					if released {
						t.Error("units released while container exists")
					}
					if args[0] == "run" {
						for _, arg := range args {
							if strings.HasSuffix(arg, ",dst=/work,readonly") {
								copyDir = strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
							}
						}
						if _, err := os.Stat(filepath.Join(copyDir, "go.mod")); err != nil {
							t.Fatal("copy absent at run:", err)
						}
						var err error
						copyLock, err = os.Open(filepath.Dir(copyDir))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = copyLock.Close() })
						if err := unix.Flock(int(copyLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
							t.Error("copy lock absent during run")
						}
						if !slices.Contains(args, "argonreview.slot=2") {
							t.Error("reservation slot did not reach run")
						}
						events = append(events, "copy", "run")
						if outcome == "interrupted" {
							cancel()
							return -1, runCtx.Err()
						}
						if outcome == "fail" {
							return 1, nil
						}
						if check == "test" || check == "test-db" {
							_, _ = io.WriteString(stdout, containerEvents("p", "TestOne", "pass"))
						}
						return 0, nil
					}
					if _, err := os.Stat(copyDir); err != nil {
						t.Error("copy removed before container cleanup")
					}
					events = append(events, args[0])
					if args[0] == "rm" {
						removed = true
						if outcome == "removal fails" {
							return 1, errors.New("removal unconfirmed")
						}
					}
					return 0, nil
				}}
				result := containerExecution{budget: budget}
				report, err := runContainerExecution(ctx, tree, check, "./...", runner, parent, &result)
				if !released || !reflect.DeepEqual(events, []string{"units taken", "copy", "run", "stop", "rm", "copy removed", "release"}) {
					t.Fatalf("budget order: %v", events)
				}
				if (err != nil) != (outcome == "removal fails" || outcome == "interrupted") {
					t.Fatalf("cleanup/interruption error: %v", err)
				}
				if outcome != "pass" && strings.HasPrefix(report, "verified") {
					t.Fatalf("failed execution verified: %s", report)
				}
			})
		}
	}
}

func TestContainerBudgetRefusal(t *testing.T) {
	for _, row := range []struct {
		name      string
		cause     budgetResult
		nilBudget bool
	}{
		{"busy", budgetBusy, false}, {"unavailable", budgetStateUnknown, false},
		{"paused", budgetOtherResult(otherExclusiveLine), false}, {"unsafe locks", budgetUnsafeLock, false},
		{"bad request", budgetBadRequest, false}, {"interrupted lock wait", budgetCanceled, false},
		{"nil budget", budgetStateUnknown, true},
	} {
		for _, check := range []string{"build", "vet", "test", "generated", "test-db", "writeback"} {
			t.Run(row.name+"/"+check, func(t *testing.T) {
				tree, parent := containerFixture(t), t.TempDir()
				runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
					t.Error("Docker reached after budget refusal")
					return 0, nil
				}}
				var budget containerBudget = fakeContainerBudget(func(context.Context, budgetRequest) (budgetReservation, budgetResult) {
					assertNoContainerCopy(t, parent)
					return budgetReservation{}, row.cause
				})
				if row.nilBudget {
					budget = nil
				}
				result := containerExecution{budget: budget}
				report, err := runContainerExecution(context.Background(), tree, check, "./...", runner, parent, &result)
				line := row.cause.line("verification budget")
				if err != nil || report != line+"\n"+containerCheckScope(check)+"\n" || result.code == 0 {
					t.Fatalf("budget refusal: %q, code %d, error %v", report, result.code, err)
				}
				if !strings.HasPrefix(result.summary, line+"\n") || !strings.Contains(result.summary, "Scope: ") || len(result.summary) > 2048 {
					t.Fatalf("refusal summary: %q", result.summary)
				}
				text, bad := verifyResult(result.summary, err, false, false)
				if !bad || !strings.HasPrefix(text, line+"\n") || len(text) > 7000 {
					t.Fatalf("MCP refusal: %q, isError %v", text, bad)
				}
				assertNoContainerCopy(t, parent)
			})
		}
	}
}

func TestContainerBudgetConstructor(t *testing.T) {
	for _, row := range []struct {
		name, kind, label, line string
	}{
		{"dev holds slot", "dev", "1", ""},
		{"verification counted once", "verification", "1", ""},
		{"M2 refusal", "unregistered", "", otherUnregisteredLine},
	} {
		t.Run(row.name, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			runner := &budgetRecordingRunner{t: t, respond: func(args []string, stdout, _ io.Writer) (int, error) {
				if args[0] == "ps" {
					_, _ = fmt.Fprintln(stdout, id)
				} else {
					c := otherFixture(row.kind, "0", "running")
					c.ID = id
					return 0, json.NewEncoder(stdout).Encode(c)
				}
				return 0, nil
			}}
			budget := newContainerBudget(runner)
			if budget.budgetHook == nil {
				t.Fatal("production constructor omitted budgetOtherHook")
			}
			// Keep the production inventory and hook; isolate host processes/locks.
			fixture, _, processes := budgetSlotsFixture(t)
			budget.processes, budget.locks = processes, fixture.locks
			if row.line != "" {
				budgetAssertCause(t, budget, strings.TrimPrefix(row.line, "NOT VERIFIED: verification budget "))
			} else {
				budgetAssertReserve(t, budget, budgetSingle, row.label).release()
			}
		})
	}
}

func TestContainerBudgetEntryPoints(t *testing.T) {
	for _, entry := range []string{"container-check", "container-generate", "verify-mcp"} {
		t.Run(entry, func(t *testing.T) {
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Error("Docker reached after unsafe locks")
				return 0, nil
			}}
			budget := newContainerBudget(runner)
			inventory, ok := budget.inventory.(budgetDockerInventory)
			if !ok || reflect.ValueOf(inventory.runner.(fakeContainerRunner).runFn).Pointer() != reflect.ValueOf(runner.runFn).Pointer() {
				t.Fatal("production inventory runner missing")
			}
			if _, ok := budget.processes.(psTable); !ok {
				t.Fatal("production process table missing")
			}
			locks := newBudgetLocks()
			if budget.locks.paths != locks.paths || budget.locks.uid != os.Getuid() || budget.locks.limit != 120*time.Second || budget.locks.poll != time.Second || budget.locks.now == nil || budget.locks.sleep == nil || budget.budgetHook == nil || reflect.ValueOf(budget.budgetHook).Pointer() != reflect.ValueOf(budgetOtherHook).Pointer() {
				t.Fatal("production budget shape changed")
			}
			v := newContainerVerifier("worktree")
			wired, ok := v.budget.(budgetSlots)
			if !ok || wired.inventory.(budgetDockerInventory).runner != v.runner {
				t.Fatal("production verifier budget missing or wrong runner")
			}
			// Keep real lock checks, but isolate discovery from host processes/Docker.
			budget.inventory = &budgetFakeInventory{}
			budget.processes = &budgetFakeProcesses{}
			budget.locks.paths[budgetSlot0] = filepath.Join(t.TempDir(), "missing", "slot-0.lock")
			tree, parent := writebackFixture(t, "sha1"), t.TempDir()
			var out, diagnostic bytes.Buffer
			code := 1
			switch entry {
			case "container-check":
				code = startContainerCheck([]string{tree, "build", "./..."}, &out, &diagnostic, nil, noSweep, runner, parent, budget)
			case "container-generate":
				code = startContainerGenerate(tree, &out, &diagnostic, nil, noSweep, runner, parent, nil, budget)
			case "verify-mcp":
				v.worktree, v.runner, v.copyParent, v.budget = tree, runner, parent, budget
				text := deadlineServe(t, verifyMCPServer{verifier: v}, "build", func() {})
				out.WriteString(text)
			}
			if code == 0 || !strings.HasPrefix(out.String(), budgetUnsafeLock.line("verification budget")+"\n") {
				t.Fatalf("entry point %s: code %d, report %q", entry, code, out.String())
			}
			assertNoContainerCopy(t, parent)
		})
	}
}

func TestContainerBudgetWait(t *testing.T) {
	for _, final := range []budgetResult{budgetBusy, budgetStateUnknown, budgetOtherResult(otherExclusiveLine), budgetCanceled} {
		t.Run(string(final), func(t *testing.T) {
			b, i, _ := budgetSlotsFixture(t)
			b.budgetHook = budgetOtherHook
			now := time.Unix(0, 0)
			start := now
			b.locks = newBudgetLocks()
			dir := t.TempDir()
			for lock := range b.locks.paths {
				b.locks.paths[lock] = filepath.Join(dir, fmt.Sprintf("lock-%d", lock))
			}
			for _, path := range b.locks.paths {
				if strings.HasPrefix(path, "/var/cache") {
					t.Fatalf("host lock path: %s", path)
				}
			}
			b.locks.now = func() time.Time { return now }
			polls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.locks.sleep = func(ctx context.Context, d time.Duration) error {
				if d != time.Second {
					t.Errorf("poll interval %v", d)
				}
				polls++
				if polls > 120 {
					return errors.New("wait exceeded bound")
				}
				now = now.Add(d)
				if polls == 119 {
					i.err = nil
				}
				if final == budgetCanceled {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			i.err = errors.New("earlier unavailable cause")
			i.read = func(int) {
				if polls < 119 {
					return
				}
				switch final {
				case budgetBusy:
					i.containers = nil
					for slot := 0; slot < 3; slot++ {
						c := otherFixture("verification", fmt.Sprint(slot), "exited")
						i.containers = append(i.containers, budgetContainer{ID: fmt.Sprintf("%064x", slot+1), Name: c.Name, Image: c.Config.Image, State: c.State.Status, Labels: c.Config.Labels, Memory: c.HostConfig.Memory, MemorySwap: c.HostConfig.MemorySwap})
					}
				case budgetStateUnknown:
					i.err = errors.New("final unavailable cause")
				default:
					c := otherFixture("exclusive", "", "created")
					i.containers = []budgetContainer{{ID: strings.Repeat("a", 64), Name: c.Name, State: c.State.Status, Labels: c.Config.Labels}}
				}
			}
			parent := t.TempDir()
			runner := fakeContainerRunner{func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
				t.Error("Docker reached during wait refusal")
				return 0, nil
			}}
			result := containerExecution{budget: b}
			report, err := runContainerExecution(ctx, containerFixture(t), "build", "./...", runner, parent, &result)
			if final == budgetCanceled {
				if !errors.Is(err, context.Canceled) || report != "" || result.summary != "" {
					t.Fatalf("wait interruption changed verdict/error: %q, %v", report, err)
				}
				text, bad := verifyResult(result.summary, err, false, true)
				if !bad || !strings.HasPrefix(text, "NOT VERIFIED: cancelled\n") {
					t.Fatalf("MCP cancellation: %q", text)
				}
			} else if polls != 120 || now.Sub(start) != 120*time.Second || err != nil || !strings.HasPrefix(report, final.line("verification budget")+"\n") {
				t.Fatalf("wait/final cause: polls %d, elapsed %v, report %q, error %v", polls, now.Sub(start), report, err)
			}
			assertNoContainerCopy(t, parent)
		})
	}
}

func TestContainerSweepIgnoresSlotLabel(t *testing.T) {
	var baselineCalls, baselineReport string
	for _, label := range []string{"", "2", "0,2", "invalid"} {
		t.Run("label="+label, func(t *testing.T) {
			s, out, dir := sweepFixture(t)
			c := sweepContainerFixture(t, s, dir)
			if label != "" {
				c.Config.Labels[budgetSlotLabel] = label
			}
			saveSweepContainer(t, dir, c, ".json")
			if err := s.run(); err != nil {
				t.Fatal(err)
			}
			calls := sweepCalls(t, dir)
			if label == "" {
				baselineCalls, baselineReport = calls, out.String()
			}
			if calls != baselineCalls || out.String() != baselineReport || !strings.Contains(calls, "rm -f "+c.ID+"\n") || !strings.Contains(out.String(), "reclaimed container "+c.ID) {
				t.Fatalf("slot label affected sweep: calls %q, report %q", calls, out.String())
			}
		})
	}
}

// Command trust gates prevent exercising the production command bodies with
// fake Docker. Pin their budget construction too, then exercise the starters
// above with real unsafe-lock refusal and isolated discovery.
func TestContainerBudgetProductionWiring(t *testing.T) {
	for _, row := range []struct{ file, entry, start string }{
		{"container.go", "containerCheckCommand", "startContainerCheck"},
		{"container_generate.go", "containerGenerateCommand", "startContainerGenerate"},
	} {
		t.Run(row.entry, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), row.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			wired := false
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != row.entry {
					continue
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					name, ok := call.Fun.(*ast.Ident)
					if !ok || name.Name != row.start {
						return true
					}
					budget, ok := call.Args[len(call.Args)-1].(*ast.CallExpr)
					if !ok {
						return true
					}
					constructor, ok := budget.Fun.(*ast.Ident)
					if !ok || constructor.Name != "newContainerBudget" || len(budget.Args) != 1 {
						return true
					}
					runner, ok := budget.Args[0].(*ast.CompositeLit)
					if !ok {
						return true
					}
					kind, ok := runner.Type.(*ast.Ident)
					wired = ok && kind.Name == "dockerRunner" && len(runner.Elts) == 0
					return true
				})
			}
			if !wired {
				t.Fatal("production command budget missing or wrong runner")
			}
		})
	}
}

func TestContainerBudgetLeftover(t *testing.T) {
	b, _ := budgetCombinedFixture(t, otherFixture("dev", "0", "running"), otherFixture("dev", "1", "running"))
	i := b.inventory.(*budgetFakeInventory)
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
		if args[0] == "run" {
			if !slices.Contains(args, "argonreview.slot=2") {
				t.Error("leftover slot missing")
			}
			c := otherFixture("verification", "2", "exited")
			i.containers = append(i.containers, budgetContainer{ID: strings.Repeat("f", 64), Name: c.Name, Image: c.Config.Image, State: c.State.Status, Labels: c.Config.Labels, Memory: c.HostConfig.Memory, MemorySwap: c.HostConfig.MemorySwap})
		}
		if args[0] == "rm" {
			return 1, errors.New("removal failed")
		}
		return 0, nil
	}}
	parent := t.TempDir()
	_, err := runContainerCheck(context.Background(), containerFixture(t), "build", "./...", runner, parent, b)
	if err == nil || !strings.Contains(err.Error(), "container removal failed") {
		t.Fatalf("removal error: %v", err)
	}
	assertNoContainerCopy(t, parent)
	// Process locks have been released; reconciliation still holds the label.
	release, cause := b.locks.acquire(context.Background(), []budgetLock{budgetSlot2})
	if cause != "" {
		t.Fatalf("slot lock retained after removal result: %s", cause)
	}
	release()
	budgetAssertCause(t, b, string(budgetBusy))
}
