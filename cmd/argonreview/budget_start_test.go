package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func budgetStartTarget() budgetContainer {
	return budgetContainer{ID: strings.Repeat("c", 64), Name: "/SECRET-NAME", Image: "postgres:18", State: "created",
		Labels: map[string]string{otherBudgetLabel: "dev", budgetSlotLabel: "2", "poison": "SECRET-LABEL"}, Memory: 1 << 30, MemorySwap: 1 << 30}
}

func budgetStartClone(c budgetContainer) budgetContainer { c.Labels = maps.Clone(c.Labels); return c }

type budgetStartFixture struct {
	started                                         bool
	missingMemory                                   bool
	snapshotFailure                                 bool
	start                                           budgetStart
	first, snapshot, post                           budgetContainer
	others                                          []budgetContainer
	calls                                           [][]string
	inspection                                      int
	inspectFailure, listFailure, startFailure, omit bool
	postFailure, stopFailure                        bool
	postContext                                     func(context.Context)
	beforeStart                                     func()
}

func newBudgetStartFixture(t *testing.T) *budgetStartFixture {
	t.Helper()
	f := &budgetStartFixture{first: budgetStartTarget(), snapshot: budgetStartTarget(), post: budgetStartTarget()}
	f.post.State = "running"
	runner := containerRunnerFunc(func(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
		if !slices.Equal(env, dockerEnvironment()) {
			t.Error("Docker environment changed")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("Docker command has no deadline")
		}
		f.calls = append(f.calls, slices.Clone(args))
		switch args[0] {
		case "ps":
			if !slices.Equal(args, []string{"ps", "-a", "-q", "--no-trunc"}) {
				t.Fatalf("listing: %v", args)
			}
			if f.listFailure {
				return 1, errors.New("SECRET-LIST")
			}
			if !f.omit {
				_, _ = fmt.Fprintln(stdout, f.first.ID)
			}
			for _, c := range f.others {
				_, _ = fmt.Fprintln(stdout, c.ID)
			}
		case "inspect":
			if len(args) != 4 || !slices.Equal(args[:3], []string{"inspect", "--format", "{{json .}}"}) {
				t.Fatalf("inspect: %v", args)
			}
			id := args[3]
			c := f.snapshot
			if id == f.first.ID {
				f.inspection++
				if f.inspection == 1 {
					c = f.first
				}
				if f.started {
					if f.postContext != nil {
						f.postContext(ctx)
					}
					c = f.post
				}
				if f.inspection == 1 && f.inspectFailure || f.started && f.postFailure {
					return 1, errors.New("SECRET-INSPECT")
				}
			} else {
				found := false
				for _, other := range f.others {
					if other.ID == id {
						c = other
						found = true
					}
				}
				if !found {
					t.Fatalf("foreign inspection: %v", args)
				}
			}
			if f.snapshotFailure && f.inspection > 1 && !f.started {
				return 1, errors.New("SECRET-SNAPSHOT")
			}
			if f.missingMemory {
				_, _ = fmt.Fprintf(stdout, `{ "Id":%q,"Name":"/SECRET-NAME","Config":{"Image":"postgres:18","Labels":{"argonreview.budget":"dev","argonreview.slot":"2"}},"State":{"Status":"created"},"HostConfig":{"MemorySwap":1073741824}}`, c.ID)
			} else {
				budgetStartEncode(t, stdout, c)
			}
		case "start":
			f.started = true
			if !slices.Equal(args, []string{"start", f.first.ID}) {
				t.Errorf("start must use exact ID: %v", args)
			}
			if f.beforeStart != nil {
				f.beforeStart()
			}
			_, _ = fmt.Fprintln(stdout, "SECRET-START")
			_, _ = fmt.Fprintln(stderr, "SECRET-ERROR")
			if f.startFailure {
				return 1, errors.New("SECRET-START-ERROR")
			}
		case "stop":
			if !slices.Equal(args, []string{"stop", "--time", "2", f.first.ID}) {
				t.Errorf("foreign cleanup: %v", args)
			}
			if f.stopFailure {
				_, _ = fmt.Fprintln(stderr, "SECRET-STOP")
				return 1, errors.New("SECRET-STOP-ERROR")
			}
		default:
			t.Fatalf("forbidden mutation: %v", args)
		}
		return 0, nil
	})
	f.start = newBudgetStart()
	f.start.inventory = budgetDockerInventory{runner}
	f.start.runner = budgetStartDocker{runner}
	f.start.processes = &budgetFakeProcesses{}
	f.start.locks = budgetLockFixture(t)
	return f
}

func budgetStartEncode(t *testing.T, w io.Writer, c budgetContainer) {
	t.Helper()
	var item otherInspect
	item.ID, item.Name = c.ID, c.Name
	item.Config.Image, item.Config.Labels = c.Image, c.Labels
	item.State.Status = c.State
	item.HostConfig.Memory, item.HostConfig.MemorySwap = c.Memory, c.MemorySwap
	if err := json.NewEncoder(w).Encode(item); err != nil {
		t.Fatal(err)
	}
}

func (f *budgetStartFixture) mutations() [][]string {
	var calls [][]string
	for _, args := range f.calls {
		if args[0] != "ps" && args[0] != "inspect" {
			calls = append(calls, args)
		}
	}
	return calls
}

func budgetStartAssert(t *testing.T, f *budgetStartFixture, args []string, code int, check string, mutations [][]string) {
	t.Helper()
	var out, err bytes.Buffer
	got := f.start.command(context.Background(), args, &out, &err)
	wantOut := ""
	if code == 0 {
		wantOut = f.first.ID + "\n"
	}
	wantErr := ""
	if code == 1 {
		wantErr = "argonreview: budget-start " + check + "\n"
	}
	if code == 2 {
		wantErr = "usage: argonreview budget-start --container <full 64-hex ID>\n"
	}
	if got != code || out.String() != wantOut || err.String() != wantErr {
		t.Errorf("outcome: code=%d out=%q err=%q; want %d %q %q", got, out.String(), err.String(), code, wantOut, wantErr)
	}
	if strings.Contains(out.String()+err.String(), "SECRET") {
		t.Error("Docker-controlled text escaped")
	}
	if !slices.EqualFunc(f.mutations(), mutations, slices.Equal[[]string]) {
		t.Errorf("mutations=%v; want %v", f.mutations(), mutations)
	}
	probe := f.start.locks
	probe.limit = 0
	probe.acquired = nil
	release, cause := probe.acquire(context.Background(), []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2})
	if cause != "" {
		t.Errorf("locks leaked: %s", cause)
	} else {
		release()
	}
}

func TestBudgetStart(t *testing.T) {
	for _, row := range []struct {
		name string
		args []string
	}{
		{"no", nil}, {"missing-id", []string{"--container"}}, {"extra", []string{"--container", strings.Repeat("c", 64), "extra"}},
		{"duplicate", []string{"--container", strings.Repeat("c", 64), "--container", strings.Repeat("c", 64)}},
		{"short", []string{"--container", "cccc"}}, {"uppercase", []string{"--container", strings.Repeat("C", 64)}},
		{"non-hex", []string{"--container", strings.Repeat("g", 64)}}, {"option-like", []string{"--container", "--help"}},
		{"wrong-option", []string{"--slot", strings.Repeat("c", 64)}}, {"command", []string{"start", "--container", strings.Repeat("c", 64)}},
	} {
		t.Run("usage/"+row.name, func(t *testing.T) {
			f := newBudgetStartFixture(t)
			budgetStartAssert(t, f, row.args, 2, "", nil)
			if len(f.calls) != 0 {
				t.Error("usage reached Docker")
			}
			var out, err bytes.Buffer
			if got := run(append([]string{"budget-start"}, row.args...), nil, &out, &err); got != 2 || out.Len() != 0 {
				t.Errorf("run usage: %d %q", got, out.String())
			}
		})
	}
	for _, row := range []struct {
		name, slot string
		locks      []budgetLock
	}{
		{"dev", "2", []budgetLock{budgetSlot2}}, {"two-slot-dev", "0,2", []budgetLock{budgetSlot0, budgetSlot2}},
		{"exclusive", "", []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newBudgetStartFixture(t)
			f.first.Labels[budgetSlotLabel] = row.slot
			if row.name == "exclusive" {
				f.first.Labels[otherBudgetLabel] = "exclusive"
				delete(f.first.Labels, budgetSlotLabel)
			}
			f.snapshot = budgetStartClone(f.first)
			f.post = budgetStartClone(f.first)
			f.post.State = "running"
			var acquired []budgetLock
			f.start.locks.acquired = func(k budgetLock, _ *os.File) { acquired = append(acquired, k) }
			f.beforeStart = func() {
				if f.inspection != 2 {
					t.Error("start preceded full snapshot")
				}
				if !slices.Equal(acquired, row.locks) {
					t.Errorf("lock set/order=%v; want %v", acquired, row.locks)
				}
				probe := f.start.locks
				probe.limit = 0
				probe.acquired = nil
				for _, lock := range row.locks {
					if r, c := probe.acquire(context.Background(), []budgetLock{lock}); c != budgetBusy {
						if r != nil {
							r()
						}
						t.Errorf("start without lock %d", lock)
					}
				}
			}
			budgetStartAssert(t, f, []string{"--container", f.first.ID}, 0, "", [][]string{{"start", f.first.ID}})
		})
	}
	for _, row := range []struct {
		name, check      string
		change           func(*budgetStartFixture)
		started, stopped bool
	}{
		{"running", "target registration check failed", func(f *budgetStartFixture) { f.first.State = "running" }, false, false},
		{"exited", "target registration check failed", func(f *budgetStartFixture) { f.first.State = "exited" }, false, false},
		{"verification-name", "target registration check failed", func(f *budgetStartFixture) { f.first.Name = budgetContainerFixture("2").Name }, false, false},
		{"owner-label", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[containerOwnerLabel] = "" }, false, false},
		{"missing-budget", "target registration check failed", func(f *budgetStartFixture) { delete(f.first.Labels, otherBudgetLabel) }, false, false},
		{"unknown-budget", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[otherBudgetLabel] = "SECRET-LABEL" }, false, false},
		{"exclusive-empty-slot", "target registration check failed", func(f *budgetStartFixture) {
			f.first.Labels[otherBudgetLabel] = "exclusive"
			f.first.Labels[budgetSlotLabel] = ""
		}, false, false},
		{"exclusive-slot", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[otherBudgetLabel] = "exclusive" }, false, false},
		{"missing-memory", "initial inspection failed", func(f *budgetStartFixture) { f.missingMemory = true }, false, false},
		{"snapshot-inspect-error", "snapshot inspection failed", func(f *budgetStartFixture) { f.snapshotFailure = true }, false, false},
		{"zero-memory", "target registration check failed", func(f *budgetStartFixture) { f.first.Memory = 0; f.first.MemorySwap = 0 }, false, false},
		{"negative-memory", "target registration check failed", func(f *budgetStartFixture) { f.first.Memory = -1; f.first.MemorySwap = -1 }, false, false},
		{"swap", "target registration check failed", func(f *budgetStartFixture) { f.first.MemorySwap++ }, false, false},
		{"over-limit", "target registration check failed", func(f *budgetStartFixture) { f.first.Memory = budgetUnit + 1; f.first.MemorySwap = f.first.Memory }, false, false},
		{"no-slot", "target registration check failed", func(f *budgetStartFixture) { delete(f.first.Labels, budgetSlotLabel) }, false, false},
		{"hostile-slot", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[budgetSlotLabel] = "../../SECRET-NAME" }, false, false},
		{"descending", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[budgetSlotLabel] = "2,0" }, false, false},
		{"slot-3", "target registration check failed", func(f *budgetStartFixture) { f.first.Labels[budgetSlotLabel] = "3" }, false, false},
		{"inspect-error", "initial inspection failed", func(f *budgetStartFixture) { f.inspectFailure = true }, false, false},
		{"listing-error", "snapshot inspection failed", func(f *budgetStartFixture) { f.listFailure = true }, false, false},
		{"missing-target", "target missing or duplicated under locks", func(f *budgetStartFixture) { f.omit = true }, false, false},
		{"changed-label", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.Labels[budgetSlotLabel] = "1" }, false, false},
		{"changed-to-exclusive", "target changed under locks", func(f *budgetStartFixture) {
			f.snapshot.Labels[otherBudgetLabel] = "exclusive"
			delete(f.snapshot.Labels, budgetSlotLabel)
		}, false, false},
		{"changed-memory", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.Memory-- }, false, false},
		{"changed-swap", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.MemorySwap-- }, false, false},
		{"changed-state", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.State = "running" }, false, false},
		{"changed-image", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.Image = "SECRET-IMAGE" }, false, false},
		{"changed-name", "target changed under locks", func(f *budgetStartFixture) { f.snapshot.Name = "/changed" }, false, false},
		{"post-state", "post-start inspection failed", func(f *budgetStartFixture) { f.post.State = "exited" }, true, true},
		{"post-label", "post-start inspection failed", func(f *budgetStartFixture) { f.post.Labels[budgetSlotLabel] = "1" }, true, true},
		{"post-memory", "post-start inspection failed", func(f *budgetStartFixture) { f.post.Memory-- }, true, true},
		{"post-swap", "post-start inspection failed", func(f *budgetStartFixture) { f.post.MemorySwap-- }, true, true},
		{"post-error", "post-start inspection failed", func(f *budgetStartFixture) { f.postFailure = true }, true, true},
		{"post-mismatch-stop-error", "post-start inspection failed; stop failed", func(f *budgetStartFixture) { f.post.State = "exited"; f.stopFailure = true }, true, true},
		{"start-error-stop-error", "start failed; stop failed", func(f *budgetStartFixture) { f.startFailure = true; f.stopFailure = true }, true, true},
		{"start-error-running", "start failed", func(f *budgetStartFixture) { f.startFailure = true }, true, true},
		{"start-error-not-running", "post-start inspection failed", func(f *budgetStartFixture) { f.startFailure = true; f.post.State = "created" }, true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newBudgetStartFixture(t)
			row.change(f)
			var acquired []budgetLock
			f.start.locks.acquired = func(k budgetLock, _ *os.File) { acquired = append(acquired, k) }
			var mutations [][]string
			if row.started {
				mutations = append(mutations, []string{"start", f.first.ID})
			}
			if row.stopped {
				mutations = append(mutations, []string{"stop", "--time", "2", f.first.ID})
			}
			budgetStartAssert(t, f, []string{"--container", f.first.ID}, 1, row.check, mutations)
			if (row.check == "target registration check failed" || row.check == "initial inspection failed") && len(acquired) != 0 {
				t.Errorf("invalid registration selected locks: %v", acquired)
			}
			if row.name == "changed-to-exclusive" && !slices.Equal(acquired, []budgetLock{budgetSlot2}) {
				t.Errorf("locks did not follow initial dev labels: %v", acquired)
			}
		})
	}
	for _, class := range []string{"dev", "exclusive"} {
		for _, failure := range []string{"inspect", "list"} {
			t.Run(class+"/"+failure, func(t *testing.T) {
				f := newBudgetStartFixture(t)
				if class == "exclusive" {
					f.first.Labels[otherBudgetLabel] = class
					delete(f.first.Labels, budgetSlotLabel)
					f.snapshot = budgetStartClone(f.first)
				}
				check := "initial inspection failed"
				if failure == "inspect" {
					f.inspectFailure = true
				} else {
					f.listFailure = true
					check = "snapshot inspection failed"
				}
				budgetStartAssert(t, f, []string{"--container", f.first.ID}, 1, check, nil)
			})
		}
	}
}

func TestBudgetStartAllowlist(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, args := range [][]string{nil, {"rm", id}, {"kill", id}, {"pause", id}, {"update", id}, {"run", id}, {"create", id}, {"stop", id}, {"stop", "--time", "3", id}, {"start", "name"}, {"start", "aaaa"}, {"start", id, "extra"}, {"stop", "--time", "2", id, "extra"}, {"start"}, {"start", strings.Repeat("A", 64)}, {"start", id}, {"stop", "--time", "2", id}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			calls := 0
			adapter := budgetStartDocker{containerRunnerFunc(func(_ context.Context, got, env []string, _, _ io.Writer) (int, error) {
				calls++
				if !slices.Equal(got, args) || !slices.Equal(env, dockerEnvironment()) {
					t.Error("adapter changed call")
				}
				return 0, nil
			})}
			err := adapter.mutate(context.Background(), args...)
			valid := slices.Equal(args, []string{"start", id}) || slices.Equal(args, []string{"stop", "--time", "2", id})
			if valid && (err != nil || calls != 1) || !valid && (err == nil || calls != 0) {
				t.Errorf("allowlist: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBudgetStartTrust(t *testing.T) {
	t.Setenv("RIBBITTO_ARGON_SOURCE", "SECRET-SOURCE")
	var out, err bytes.Buffer
	if code := run([]string{"budget-start", "--container", budgetStartTarget().ID}, nil, &out, &err); code != 1 || out.Len() != 0 || err.String() != "argonreview: budget-start trusted build check failed\n" {
		t.Fatalf("untrusted dispatch: %d %q %q", code, out.String(), err.String())
	}
}

func TestBudgetStartMutationLimits(t *testing.T) {
	for _, kind := range []string{"exit", "error", "stdout-cap", "stderr-cap", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			runner := containerRunnerFunc(func(ctx context.Context, _, env []string, stdout, stderr io.Writer) (int, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok || !slices.Equal(env, dockerEnvironment()) {
					t.Error("mutation lacks deadline or fixed environment")
				}
				switch kind {
				case "exit":
					return 1, nil
				case "error":
					return 0, errors.New("SECRET-ERROR")
				case "stdout-cap":
					_, _ = io.WriteString(stdout, strings.Repeat("x", containerOutputLimit+1))
				case "stderr-cap":
					_, _ = io.WriteString(stderr, strings.Repeat("x", containerOutputLimit+1))
				}
				return 0, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			err := (budgetStartDocker{runner}).mutate(ctx, "start", budgetStartTarget().ID)
			if err == nil || err.Error() != "container mutation failed" {
				t.Errorf("unbounded or disclosed result: %v", err)
			}
			if kind == "cancelled" && calls != 0 {
				t.Error("cancelled mutation reached Docker")
			}
		})
	}
}

func TestBudgetStartReadOrder(t *testing.T) {
	f := newBudgetStartFixture(t)
	p := f.start.processes.(*budgetFakeProcesses)
	p.listRead = func(int) {
		if f.inspection != 1 || len(f.calls) != 1 || f.calls[0][0] != "inspect" {
			t.Error("snapshot preceded process discovery")
		}
		probe := f.start.locks
		probe.limit = 0
		release, cause := probe.acquire(context.Background(), []budgetLock{budgetSlot2})
		if release != nil {
			release()
		}
		if cause != budgetBusy {
			t.Error("process discovery occurred outside target locks")
		}
	}
	budgetStartAssert(t, f, []string{"--container", f.first.ID}, 0, "", [][]string{{"start", f.first.ID}})
	if p.reads != 1 {
		t.Errorf("process reads=%d; want 1", p.reads)
	}
}

func TestBudgetStartPostStartContext(t *testing.T) {
	for _, kind := range []string{"past-wait-deadline", "parent-canceled-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := newBudgetStartFixture(t)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.start.devWait = time.Second
			now := time.Now()
			f.start.locks.now = func() time.Time { return now }
			f.beforeStart = func() {
				now = now.Add(2 * time.Second)
				if kind == "parent-canceled-mismatch" {
					cancel()
				}
			}
			inspected := false
			f.postContext = func(ctx context.Context) {
				inspected = true
				deadline, ok := ctx.Deadline()
				if ctx.Err() != nil || !ok || time.Until(deadline) <= f.start.devWait || time.Until(deadline) > containerCleanupTimeout {
					t.Errorf("post-start inspection lacks a fresh bounded context: deadline=%v err=%v", deadline, ctx.Err())
				}
			}
			wantCode, wantOut, wantErr := 0, f.first.ID+"\n", ""
			wantMutations := [][]string{{"start", f.first.ID}}
			if kind == "parent-canceled-mismatch" {
				f.post.State = "exited"
				wantCode, wantOut, wantErr = 1, "", "argonreview: budget-start post-start inspection failed\n"
				wantMutations = append(wantMutations, []string{"stop", "--time", "2", f.first.ID})
			}
			var out, err bytes.Buffer
			code := f.start.command(parent, []string{"--container", f.first.ID}, &out, &err)
			if code != wantCode || out.String() != wantOut || err.String() != wantErr || !inspected {
				t.Errorf("post-start outcome: code=%d out=%q err=%q inspected=%v", code, out.String(), err.String(), inspected)
			}
			if !slices.EqualFunc(f.mutations(), wantMutations, slices.Equal[[]string]) {
				t.Errorf("mutations=%v; want %v", f.mutations(), wantMutations)
			}
			budgetTake(t, f.start.locks, budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2)()
		})
	}
}
