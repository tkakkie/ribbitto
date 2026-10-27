package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type budgetFakeInventory struct {
	containers []budgetContainer
	err        error
	reads      int
	read       func(int)
}

func (b *budgetFakeInventory) budgetList(context.Context) ([]budgetContainer, error) {
	b.reads++
	if b.read != nil {
		b.read(b.reads)
	}
	return b.containers, b.err
}

// Any process read or signal is a test failure through the nil embedded table.
type budgetFakeProcesses struct {
	processTable
	procs    []procInfo
	err      error
	reads    int
	listRead func(int)
}

func (b *budgetFakeProcesses) list() ([]procInfo, error) {
	b.reads++
	if b.listRead != nil {
		b.listRead(b.reads)
	}
	return b.procs, b.err
}

func budgetContainerFixture(label string) budgetContainer {
	return budgetContainer{ID: strings.Repeat("a", 64), Name: "/argonreview-check-" + strings.Repeat("a", 32),
		State: "running", Labels: map[string]string{containerOwnerLabel: containerOwnerValue, budgetSlotLabel: label}}
}

func budgetSlotsFixture(t *testing.T) (budgetSlots, *budgetFakeInventory, *budgetFakeProcesses) {
	t.Helper()
	i, p := &budgetFakeInventory{}, &budgetFakeProcesses{}
	b := budgetSlots{inventory: i, processes: p, locks: budgetLockFixture(t)}
	b.locks.limit = 0
	return b, i, p
}

func budgetAssertReserve(t *testing.T, b budgetSlots, request budgetRequest, label string) budgetReservation {
	t.Helper()
	r, result := b.budgetReserve(context.Background(), request)
	if result != "" {
		t.Fatalf("reserve: %s", result.line("verification budget"))
	}
	t.Cleanup(r.release)
	if r.label != label || r.memory != int64(len(r.slots))*budgetUnit {
		t.Fatalf("reservation: slots=%v label=%q memory=%d; want label %q", r.slots, r.label, r.memory, label)
	}
	return r
}

func budgetAssertCause(t *testing.T, b budgetSlots, cause string) {
	t.Helper()
	r, result := b.budgetReserve(context.Background(), budgetSingle)
	if r.release != nil {
		r.release()
		t.Fatal("unexpected reservation")
	}
	if got := result.line("verification budget"); got != "NOT VERIFIED: verification budget "+cause {
		t.Fatalf("line: %q; want cause %q", got, cause)
	}
}

func TestBudgetReadOrder(t *testing.T) {
	for _, row := range []struct {
		name      string
		firstRead int
	}{
		{"pre-lock", 1},
		{"reconciliation", 3},
	} {
		t.Run(row.name, func(t *testing.T) {
			b, i, p := budgetSlotsFixture(t)
			orphan := budgetOrphanFixture(b.locks.uid)
			procs := []procInfo{orphan}
			var containers []budgetContainer
			if row.firstRead == 3 {
				procs = nil
			}
			reads := 0
			completeRead := func() {
				reads++
				if reads == row.firstRead-1 {
					procs = []procInfo{orphan}
				}
				if reads == row.firstRead {
					procs = nil
					containers = []budgetContainer{budgetContainerFixture("0")}
				}
			}
			// Snapshot each response before advancing the shared world, so the
			// transition occurs between reads regardless of their order.
			i.read = func(int) { i.containers = containers; completeRead() }
			p.listRead = func(int) { p.procs = procs; completeRead() }
			r, result := b.budgetReserve(context.Background(), budgetSingle)
			if r.release != nil {
				t.Cleanup(r.release)
			}
			want := "NOT VERIFIED: verification budget unavailable: an orphaned Docker client is present"
			if got := result.line("verification budget"); got != want || r.release != nil {
				t.Fatalf("reserve: line=%q label=%q; want line=%q and no reservation", got, r.label, want)
			}
			if reads != row.firstRead+1 {
				t.Fatalf("reads: %d; want %d", reads, row.firstRead+1)
			}
			i.read, p.listRead = nil, nil
			i.containers, p.procs = nil, nil
			budgetAssertReserve(t, b, budgetSingle, "0").release()
		})
	}
}

func TestBudgetSlots(t *testing.T) {
	if path := os.Getenv("BUDGET_SLOT_CHILD"); path != "" {
		b := newBudgetLocks()
		b.paths[budgetSlot0], b.limit = path, 0
		release, result := b.acquire(context.Background(), []budgetLock{budgetSlot0})
		if result != "" {
			t.Fatal(result)
		}
		defer release()
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	t.Run("capacity", func(t *testing.T) {
		b, _, _ := budgetSlotsFixture(t)
		for _, label := range []string{"0", "1", "2"} {
			budgetAssertReserve(t, b, budgetSingle, label)
		}
		budgetAssertCause(t, b, "busy")
	})
	for _, row := range []struct {
		name    string
		request budgetRequest
		held    string
		want    string
		locks   []budgetLock
	}{
		{"lowest", budgetSingle, "0", "1", []budgetLock{budgetSlot1}},
		{"browser-lowest", budgetBrowserRequest, "", "0,1", []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1}},
		{"browser", budgetBrowserRequest, "1", "0,2", []budgetLock{budgetBrowser, budgetSlot0, budgetSlot2}},
		{"exclusive", budgetExclusive, "", "0,1,2", []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2}},
	} {
		t.Run(row.name, func(t *testing.T) {
			b, i, _ := budgetSlotsFixture(t)
			if row.held != "" {
				i.containers = []budgetContainer{budgetContainerFixture(row.held)}
			}
			var acquired []budgetLock
			b.locks.acquired = func(k budgetLock, _ *os.File) { acquired = append(acquired, k) }
			budgetAssertReserve(t, b, row.request, row.want)
			if !slices.Equal(acquired, row.locks) {
				t.Fatalf("locks: %v", acquired)
			}
		})
	}
	t.Run("plans", func(t *testing.T) {
		for _, row := range []struct {
			held    [3]bool
			request budgetRequest
			want    [][]int
		}{
			{[3]bool{}, budgetSingle, [][]int{{0}, {1}, {2}}},
			{[3]bool{true}, budgetSingle, [][]int{{1}, {2}}},
			{[3]bool{}, budgetBrowserRequest, [][]int{{0, 1}, {0, 2}, {1, 2}}},
			{[3]bool{false, true}, budgetBrowserRequest, [][]int{{0, 2}}},
			{[3]bool{}, budgetExclusive, [][]int{{0, 1, 2}}},
			{[3]bool{true}, budgetExclusive, nil},
			{[3]bool{true, true, true}, budgetSingle, nil},
		} {
			got := budgetPlans(row.held, row.request)
			if len(got) != len(row.want) {
				t.Fatalf("plans: %v; want %v", got, row.want)
			}
			for i := range got {
				if !slices.Equal(got[i], row.want[i]) {
					t.Fatalf("plans: %v; want %v", got, row.want)
				}
			}
		}
	})
	t.Run("all-or-none", func(t *testing.T) {
		b, _, _ := budgetSlotsFixture(t)
		budgetTake(t, b.locks, budgetSlot1)
		budgetTake(t, b.locks, budgetSlot2)
		for _, request := range []budgetRequest{budgetBrowserRequest, budgetExclusive} {
			if r, result := b.budgetReserve(context.Background(), request); r.release != nil || result != budgetBusy {
				t.Fatalf("partial reservation: %s", result)
			}
			budgetTake(t, b.locks, budgetBrowser, budgetSlot0)()
		}
	})
	t.Run("reconcile-under-locks", func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		i.read = func(n int) {
			if n == 2 {
				i.containers = []budgetContainer{budgetContainerFixture("0")}
			}
		}
		budgetAssertReserve(t, b, budgetSingle, "1").release()
		budgetTake(t, b.locks, budgetSlot0)()
	})
	t.Run("SIGKILL", func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		budgetKilledHolder(t, b.locks)
		budgetTake(t, b.locks, budgetSlot0)()
		for _, state := range []string{"created", "running", "paused", "restarting", "exited", "dead", "removing"} {
			t.Run(state, func(t *testing.T) {
				c := budgetContainerFixture("0")
				c.State = state
				i.containers = []budgetContainer{c}
				budgetAssertReserve(t, b, budgetSingle, "1").release()
				i.containers = append(i.containers, budgetContainerFixture("1,2"))
				budgetAssertCause(t, b, "busy")
			})
		}
		i.containers = nil
		budgetAssertReserve(t, b, budgetSingle, "0").release()
	})
}

func budgetKilledHolder(t *testing.T, b budgetLocks) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBudgetSlots$")
	cmd.Env = append(os.Environ(), "BUDGET_SLOT_CHILD="+b.paths[budgetSlot0])
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("child readiness: %q", line)
		}
	case <-ctx.Done():
		t.Fatal("child readiness timed out")
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child not SIGKILLed: %v", err)
	}
}

func TestBudgetParseSlots(t *testing.T) {
	for _, row := range []struct {
		label string
		want  []int
	}{
		{"0", []int{0}}, {"1", []int{1}}, {"2", []int{2}},
		{"0,1", []int{0, 1}}, {"0,2", []int{0, 2}}, {"1,2", []int{1, 2}},
		{"", nil}, {"3", nil}, {"0,3", nil}, {"9", nil}, {"0,", nil},
		{"0,0", nil}, {"1,0", nil}, {"2,1", nil}, {"0,1,2", nil},
		{"a", nil}, {"0;1", nil}, {" 0", nil},
	} {
		t.Run("label="+row.label, func(t *testing.T) {
			got, valid := budgetParseSlots(row.label)
			if valid != (row.want != nil) || !slices.Equal(got, row.want) && valid {
				t.Fatalf("parse %q: %v, %v; want %v, %v", row.label, got, valid, row.want, row.want != nil)
			}
		})
	}
}

func TestBudgetContainerStates(t *testing.T) {
	for _, verification := range []bool{false, true} {
		for _, state := range []string{"", "unknown", "created", "running", "paused", "restarting", "exited", "dead", "removing"} {
			t.Run(fmt.Sprintf("verification=%v/status=%s", verification, state), func(t *testing.T) {
				b, i, _ := budgetSlotsFixture(t)
				c := budgetContainerFixture("0")
				c.State = state
				label := "1"
				if !verification {
					c.Name, c.Labels = "/foreign", nil
					label = "0"
				}
				i.containers = []budgetContainer{c}
				if state == "" || state == "unknown" {
					budgetAssertCause(t, b, "unavailable: the container state could not be determined")
				} else {
					budgetAssertReserve(t, b, budgetSingle, label).release()
				}
			})
		}
	}
	t.Run("precedence", func(t *testing.T) {
		b, i, p := budgetSlotsFixture(t)
		p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
		i.containers = []budgetContainer{
			{State: "unknown"}, budgetContainerFixture("invalid"),
			budgetContainerFixture("0"), budgetContainerFixture("0"),
		}
		budgetAssertCause(t, b, "unavailable: the container state could not be determined")
		slices.Reverse(i.containers)
		budgetAssertCause(t, b, "unavailable: the container state could not be determined")
	})
}

func TestBudgetUnknownStateContainers(t *testing.T) {
	for _, label := range []string{"", "3", "-1", "00", "0,0", "0,3", "1,0", "0,1,2", "0, 1", " 0", "0 ", "0;1", "x"} {
		t.Run("label="+label, func(t *testing.T) {
			b, i, _ := budgetSlotsFixture(t)
			i.containers = []budgetContainer{budgetContainerFixture(label)}
			budgetAssertCause(t, b, "unavailable: a verification container has no valid slot label")
		})
	}
	for _, row := range []struct{ name, cause string }{
		{"inventory", "the container state could not be determined"},
		{"processes", "the container state could not be determined"},
		{"missing-label", "a verification container has no valid slot label"},
		{"duplicate", "a slot is claimed twice"},
		{"orphan", "an orphaned Docker client is present"},
		{"lock-directory", "lock files are unavailable or unsafe"},
		{"lock-file", "lock files are unavailable or unsafe"},
	} {
		t.Run(row.name, func(t *testing.T) {
			b, i, p := budgetSlotsFixture(t)
			switch row.name {
			case "inventory":
				i.err = errors.New("untrusted inventory data")
			case "processes":
				p.err = errors.New("untrusted process data")
			case "missing-label":
				c := budgetContainerFixture("0")
				delete(c.Labels, budgetSlotLabel)
				delete(c.Labels, containerOwnerLabel)
				i.containers = []budgetContainer{c}
			case "duplicate":
				c := budgetContainerFixture("0")
				c.ID = strings.Repeat("b", 64)
				i.containers = []budgetContainer{budgetContainerFixture("0"), c}
			case "orphan":
				p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
			case "lock-directory":
				if err := os.Chmod(filepath.Dir(b.locks.paths[budgetSlot0]), 0o720); err != nil {
					t.Fatal(err)
				}
			case "lock-file":
				if err := os.WriteFile(b.locks.paths[budgetSlot0], nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			budgetAssertCause(t, b, "unavailable: "+row.cause)
		})
	}
	t.Run("precedence", func(t *testing.T) {
		b, i, p := budgetSlotsFixture(t)
		p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
		duplicate := budgetContainerFixture("0")
		duplicate.ID = strings.Repeat("b", 64)
		i.containers = []budgetContainer{budgetContainerFixture(""), budgetContainerFixture("0"), duplicate}
		i.err = errors.New("listing failed")
		budgetAssertCause(t, b, "unavailable: the container state could not be determined")
		i.err = nil
		budgetAssertCause(t, b, "unavailable: an orphaned Docker client is present")
		p.procs = nil
		budgetAssertCause(t, b, "unavailable: a verification container has no valid slot label")
		slices.Reverse(i.containers)
		budgetAssertCause(t, b, "unavailable: a verification container has no valid slot label")
		i.containers = i.containers[:2]
		budgetAssertCause(t, b, "unavailable: a slot is claimed twice")
		i.containers = nil
		p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
		budgetAssertCause(t, b, "unavailable: an orphaned Docker client is present")
	})
	t.Run("ownership-and-hook", func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		for _, kind := range []string{"name-only", "owner-only", "empty-owner"} {
			t.Run(kind, func(t *testing.T) {
				c := budgetContainerFixture("0")
				switch kind {
				case "name-only":
					delete(c.Labels, containerOwnerLabel)
				case "owner-only":
					c.Name = "/foreign"
				case "empty-owner":
					c.Name = "/foreign"
					c.Labels[containerOwnerLabel] = ""
				}
				i.containers = []budgetContainer{c}
				budgetAssertReserve(t, b, budgetSingle, "1").release()
			})
		}
		c := budgetContainerFixture("invalid")
		c.Name = "argonreview-check-" + strings.Repeat("a", 32)
		delete(c.Labels, containerOwnerLabel)
		i.containers = []budgetContainer{c}
		budgetAssertReserve(t, b, budgetSingle, "0").release()
		calls := 0
		i.containers = append(i.containers, budgetContainerFixture("2"))
		b.budgetHook = func(cs []budgetContainer) ([3]bool, budgetResult) {
			calls++
			if !reflect.DeepEqual(cs, i.containers) {
				t.Fatalf("hook input: %+v; want %+v", cs, i.containers)
			}
			return [3]bool{true, false, false}, ""
		}
		budgetAssertReserve(t, b, budgetSingle, "1").release()
		if calls != 2 {
			t.Fatalf("hook calls: %d", calls)
		}
	})
	for _, kind := range []string{"live", "different-uid", "unrelated"} {
		t.Run(kind, func(t *testing.T) {
			b, _, p := budgetSlotsFixture(t)
			proc := budgetOrphanFixture(b.locks.uid)
			switch kind {
			case "live":
				proc.ppid = 2
			case "different-uid":
				proc.uid++
			case "unrelated":
				proc.command = "/usr/bin/anything"
			}
			p.procs = []procInfo{proc}
			budgetAssertReserve(t, b, budgetSingle, "0").release()
		})
	}
}

func TestBudgetHook(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprintf("collision=%v", collision), func(t *testing.T) {
			b, i, _ := budgetSlotsFixture(t)
			i.containers = []budgetContainer{
				budgetContainerFixture("0"),
				{ID: "foreign", Name: "/foreign", Image: "image", State: "exited", Memory: 123, MemorySwap: 456},
			}
			b.budgetHook = func(cs []budgetContainer) ([3]bool, budgetResult) {
				if !reflect.DeepEqual(cs, i.containers) {
					t.Fatalf("hook input: %+v; want %+v", cs, i.containers)
				}
				if collision {
					return [3]bool{true}, ""
				}
				return [3]bool{false, true}, ""
			}
			for range 2 {
				if collision {
					budgetAssertCause(t, b, "unavailable: a slot is claimed twice")
				} else {
					budgetAssertReserve(t, b, budgetSingle, "2").release()
				}
				slices.Reverse(i.containers)
			}
		})
	}
}

func budgetOrphanFixture(uid int) procInfo {
	return procInfo{pid: 42, ppid: 1, uid: uid, command: "/usr/local/bin/docker run --pull=never --name argonreview-check-" + strings.Repeat("a", 32) + " --network none image"}
}

// Sample Err before canceling to reproduce cancellation between the attempt's
// final check and the caller observing its returned reservation, without a race.
type budgetCancelAtReturn struct {
	context.Context
	cancel context.CancelFunc
	armed  bool
}

func (c *budgetCancelAtReturn) Err() error {
	err := c.Context.Err()
	if c.armed {
		c.cancel()
	}
	return err
}

func TestBudgetCancellation(t *testing.T) {
	for _, kind := range []string{"busy", "unavailable", "acquired-during-read", "acquired"} {
		t.Run(kind, func(t *testing.T) {
			b, i, _ := budgetSlotsFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			boundary := &budgetCancelAtReturn{Context: ctx, cancel: cancel}
			requestContext := ctx
			switch kind {
			case "busy":
				i.containers = []budgetContainer{budgetContainerFixture("0"), budgetContainerFixture("1,2")}
				i.read = func(int) { cancel() }
			case "unavailable":
				i.err = errors.New("listing failed")
				i.read = func(int) { cancel() }
			case "acquired-during-read":
				i.read = func(n int) {
					if n == 2 {
						cancel()
					}
				}
			case "acquired":
				requestContext = boundary
				i.read = func(n int) { boundary.armed = n == 2 }
			}
			r, result := b.budgetReserve(requestContext, budgetSingle)
			if r.release != nil {
				t.Cleanup(r.release)
			}
			if result != budgetCanceled || r.release != nil || len(r.slots) != 0 || r.label != "" || r.memory != 0 {
				t.Fatalf("canceled reservation: %+v, %q", r, result)
			}
			budgetTake(t, b.locks, budgetSlot0, budgetSlot1, budgetSlot2)()
		})
	}
}

func TestBudgetResumes(t *testing.T) {
	for _, kind := range []string{"removed", "label-fixed", "orphan-gone", "lock-released", "inventory-fixed", "processes-fixed"} {
		for _, midWait := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mid-wait=%v", kind, midWait), func(t *testing.T) {
				b, i, p := budgetSlotsFixture(t)
				clear := func() { i.containers = nil; i.err = nil; p.procs = nil; p.err = nil }
				switch kind {
				case "removed":
					i.containers = []budgetContainer{budgetContainerFixture("0"), budgetContainerFixture("1,2")}
				case "label-fixed":
					i.containers = []budgetContainer{budgetContainerFixture("")}
					clear = func() { i.containers[0].Labels[budgetSlotLabel] = "2" }
				case "orphan-gone":
					p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
				case "lock-released":
					clear = budgetTake(t, b.locks, budgetSlot0, budgetSlot1, budgetSlot2)
				case "inventory-fixed":
					i.err = errors.New("listing failed")
				case "processes-fixed":
					p.err = errors.New("listing failed")
				}
				if !midWait {
					r, result := b.budgetReserve(context.Background(), budgetSingle)
					if r.release != nil || result == "" {
						t.Fatal("conflict accepted")
					}
					clear()
				} else {
					now := time.Unix(0, 0)
					b.locks.now = func() time.Time { return now }
					b.locks.limit = 2 * time.Second
					b.locks.sleep = func(_ context.Context, d time.Duration) error { clear(); now = now.Add(d); return nil }
				}
				budgetAssertReserve(t, b, budgetSingle, "0").release()
			})
		}
	}
	t.Run("bounded-last-cause", func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		i.err = errors.New("listing failed")
		now := time.Unix(0, 0)
		b.locks.now = func() time.Time { return now }
		b.locks.limit = 120 * time.Second
		polls := 0
		b.locks.sleep = func(_ context.Context, d time.Duration) error {
			polls++
			if d != time.Second || polls > 120 {
				t.Fatalf("unbounded poll: %d, %v", polls, d)
			}
			i.err = nil
			i.containers = []budgetContainer{budgetContainerFixture("")}
			now = now.Add(d)
			return nil
		}
		budgetAssertCause(t, b, "unavailable: a verification container has no valid slot label")
		if polls != 120 {
			t.Fatalf("polls: %d", polls)
		}
	})
	t.Run("release-before-sleep", func(t *testing.T) {
		b, i, _ := budgetSlotsFixture(t)
		i.read = func(n int) {
			if n == 2 {
				i.containers = []budgetContainer{budgetContainerFixture("0"), budgetContainerFixture("1,2")}
			}
		}
		b.locks.limit = time.Second
		now := time.Unix(0, 0)
		b.locks.now = func() time.Time { return now }
		sleeps := 0
		b.locks.sleep = func(_ context.Context, d time.Duration) error {
			sleeps++
			probe := b.locks
			probe.limit = 0
			budgetTake(t, probe, budgetSlot0, budgetBrowser)()
			now = now.Add(d)
			return nil
		}
		budgetAssertCause(t, b, "busy")
		if sleeps != 1 {
			t.Fatalf("sleeps: %d; want 1", sleeps)
		}
	})
	t.Run("immediate-results", func(t *testing.T) {
		b, _, _ := budgetSlotsFixture(t)
		b.locks.sleep = func(context.Context, time.Duration) error { t.Fatal("unexpected sleep"); return nil }
		if _, result := b.budgetReserve(context.Background(), 0); result != budgetBadRequest {
			t.Fatal(result)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, result := b.budgetReserve(ctx, budgetSingle); result != budgetCanceled {
			t.Fatal(result)
		}
		b.locks.limit = time.Second
		b.locks.paths[budgetSlot0] += "/missing"
		budgetAssertCause(t, b, "unavailable: lock files are unavailable or unsafe")
	})
}
