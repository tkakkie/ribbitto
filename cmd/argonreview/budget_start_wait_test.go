package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestBudgetStartWait(t *testing.T) {
	for _, class := range []string{"dev", "exclusive"} {
		for _, kind := range []string{"lock-busy", "lock-released", "leftover", "held-dev", "held-dev-0", "held-dev-1", "exclusive-created", "exclusive-running", "unregistered", "unlimited", "orphan", "mismatched-verification"} {
			t.Run(class+"/"+kind, func(t *testing.T) {
				f := newBudgetStartFixture(t)
				wantWait := 2 * time.Minute
				if class == "exclusive" {
					f.first.Labels[otherBudgetLabel] = class
					delete(f.first.Labels, budgetSlotLabel)
					wantWait = 15 * time.Minute
				}
				f.snapshot = budgetStartClone(f.first)
				f.post = budgetStartClone(f.first)
				f.post.State = "running"
				now := time.Unix(0, 0)
				began := now
				f.start.locks.now = func() time.Time { return now }
				f.start.locks.poll = time.Minute
				var opened []*os.File
				f.start.locks.acquired = func(_ budgetLock, file *os.File) { opened = append(opened, file) }
				probe := f.start.locks
				probe.limit = 0
				probe.acquired = nil
				var holder func()
				if kind == "lock-busy" || kind == "lock-released" {
					holder = budgetTake(t, probe, budgetSlot2)
				}
				c := budgetStartTarget()
				c.ID = fmt.Sprintf("%064x", 5)
				check := "busy"
				immediate := false
				switch kind {
				case "leftover", "mismatched-verification":
					c = budgetContainerFixture("2")
					c.ID = fmt.Sprintf("%064x", 5)
					c.State = "exited"
					c.Image = containerVerifyImage
					c.Memory = budgetUnit
					c.MemorySwap = budgetUnit
					if kind == "mismatched-verification" {
						c.Image = "SECRET-IMAGE"
						check = string(budgetOtherResult(otherVerificationLine))
						immediate = true
					}
					f.others = []budgetContainer{c}
				case "held-dev", "held-dev-0", "held-dev-1":
					if kind == "held-dev-0" {
						c.Labels[budgetSlotLabel] = "0"
					}
					if kind == "held-dev-1" {
						c.Labels[budgetSlotLabel] = "1"
					}
					f.others = []budgetContainer{c}
				case "exclusive-created", "exclusive-running":
					c.Labels[otherBudgetLabel] = "exclusive"
					delete(c.Labels, budgetSlotLabel)
					if kind == "exclusive-running" {
						c.State = "running"
					}
					f.others = []budgetContainer{c}
				case "unregistered":
					c.Labels = nil
					f.others = []budgetContainer{c}
					check = string(budgetOtherResult(otherUnregisteredLine))
					immediate = true
				case "unlimited":
					c.Memory = 0
					c.MemorySwap = 0
					f.others = []budgetContainer{c}
					check = string(budgetOtherResult(otherUnlimitedLine))
					immediate = true
				case "orphan":
					f.start.processes = &budgetFakeProcesses{procs: []procInfo{budgetOrphanFixture(f.start.locks.uid)}}
					check = string(budgetOrphanPresent)
					immediate = true
				}
				sleeps := 0
				f.start.locks.aboutToSleep = func() {
					for _, file := range opened {
						if file.Fd() != ^uintptr(0) {
							t.Error("start held a lock while sleeping")
						}
					}
					opened = nil
					// A live runner may hold slot 2; all earlier locks must still be free.
					locks := []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1}
					if holder == nil {
						locks = append(locks, budgetSlot2)
					}
					budgetTake(t, probe, locks...)()
				}
				f.start.locks.sleep = func(_ context.Context, d time.Duration) error {
					sleeps++
					if sleeps > 15 {
						t.Fatal("no bounded deadline")
					}
					now = now.Add(d)
					if kind == "lock-released" {
						holder()
						holder = nil
					}
					return nil
				}
				code := 1
				var mutations [][]string
				if kind == "lock-released" || class == "dev" && (kind == "held-dev-0" || kind == "held-dev-1") {
					code = 0
					mutations = [][]string{{"start", f.first.ID}}
				}
				// Release the competing runner before the common leak assertion.
				if holder != nil {
					defer holder()
				}
				if kind == "lock-busy" {
					// The assertion helper probes all locks; release the runner after the last sleep.
					f.start.locks.sleep = func(_ context.Context, d time.Duration) error {
						sleeps++
						now = now.Add(d)
						if sleeps > 15 {
							t.Fatal("no bounded deadline")
						}
						if now.Sub(began) >= wantWait {
							holder()
							holder = nil
						}
						return nil
					}
				}
				budgetStartAssert(t, f, []string{"--container", f.first.ID}, code, check, mutations)
				if immediate && (sleeps != 0 || !now.Equal(began)) {
					t.Error("unavailable cause waited")
				}
				if code != 0 && !immediate && kind != "lock-released" && now.Sub(began) != wantWait {
					t.Errorf("wait=%v; want exactly %v", now.Sub(began), wantWait)
				}
				if kind == "lock-released" && sleeps != 1 {
					t.Errorf("start did not wait for runner: sleeps=%d", sleeps)
				}
			})
		}
	}
}

func TestBudgetStartCreatedHoldsSlot(t *testing.T) {
	b, i, _ := budgetSlotsFixture(t)
	b.budgetHook = budgetOtherHook
	i.containers = []budgetContainer{budgetStartTarget()}
	budgetTake(t, b.locks, budgetSlot0, budgetSlot1)
	if r, cause := b.budgetReserve(context.Background(), budgetSingle); r.release != nil || cause != budgetBusy {
		if r.release != nil {
			r.release()
		}
		t.Fatalf("runner took created target's slot: %v", cause)
	}
}

// Every row uses the production read adapter and the strictly bounded mutation
// adapter. The shared recorder checks all commands across each failure boundary.
func TestBudgetStartNeverTouchesForeign(t *testing.T) {
	for _, kind := range []string{"success", "initial-refusal", "snapshot-refusal", "post-refusal", "start-failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newBudgetStartFixture(t)
			foreign := budgetStartTarget()
			foreign.ID = fmt.Sprintf("%064x", 9)
			foreign.Labels[budgetSlotLabel] = "0"
			f.others = []budgetContainer{foreign}
			code, check := 0, ""
			mutations := [][]string{{"start", f.first.ID}}
			switch kind {
			case "initial-refusal":
				f.first.State = "running"
				code = 1
				check = "target registration check failed"
				mutations = nil
			case "snapshot-refusal":
				f.snapshot.Image = "SECRET-IMAGE"
				code = 1
				check = "target changed under locks"
				mutations = nil
			case "post-refusal":
				f.post.State = "exited"
				code = 1
				check = "post-start inspection failed"
				mutations = append(mutations, []string{"stop", "--time", "2", f.first.ID})
			case "start-failure":
				f.startFailure = true
				f.post.State = "created"
				code = 1
				check = "post-start inspection failed"
				mutations = append(mutations, []string{"stop", "--time", "2", f.first.ID})
			}
			budgetStartAssert(t, f, []string{"--container", f.first.ID}, code, check, mutations)
		})
	}
}
