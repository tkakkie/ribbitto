package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func budgetCombinedFixture(t *testing.T, list ...otherInspect) (budgetSlots, *budgetFakeProcesses) {
	t.Helper()
	b, inventory, processes := budgetSlotsFixture(t)
	b.budgetHook = budgetOtherHook
	for i, c := range list {
		inventory.containers = append(inventory.containers, budgetContainer{
			ID: fmt.Sprintf("%064x", i+1), Name: c.Name, Image: c.Config.Image, State: c.State.Status,
			Labels: c.Config.Labels, Memory: c.HostConfig.Memory, MemorySwap: c.HostConfig.MemorySwap,
		})
	}
	return b, processes
}

func TestBudgetLines(t *testing.T) {
	for _, row := range []struct {
		result budgetResult
		line   string
	}{
		{budgetStateUnknown, otherStateLine},
		{budgetOrphanPresent, "NOT VERIFIED: verification budget unavailable: an orphaned Docker client is present"},
		{budgetOtherResult(otherVerificationLine), otherVerificationLine},
		{budgetInvalidLabel, "NOT VERIFIED: verification budget unavailable: a verification container has no valid slot label"},
		{budgetOtherResult(otherRegistrationLine), otherRegistrationLine},
		{budgetOtherResult(otherUnlimitedLine), otherUnlimitedLine},
		{budgetOtherResult(otherUnregisteredLine), otherUnregisteredLine},
		{budgetDuplicate, otherDuplicateLine},
		{budgetOtherResult(otherExclusiveLine), otherExclusiveLine},
		{budgetBusy, "NOT VERIFIED: verification budget busy"},
		{budgetUnsafeLock, "NOT VERIFIED: verification budget unavailable: lock files are unavailable or unsafe"},
		{budgetBadRequest, "NOT VERIFIED: verification budget unavailable: invalid lock set"},
		{budgetCanceled, "NOT VERIFIED: verification budget unavailable: lock wait interrupted"},
	} {
		t.Run(string(row.result), func(t *testing.T) {
			if got := row.result.line("verification budget"); got != row.line {
				t.Fatalf("line = %q; want %q", got, row.line)
			}
		})
	}
}

func TestBudgetLinePrecedence(t *testing.T) {
	verify := otherFixture("verification", "0", "running")
	dev := otherFixture("dev", "1", "running")
	descending := otherFixture("verification", "2,0", "running")
	rows := []struct {
		name, line string
		list       []otherInspect
		orphan     bool
	}{
		{"state", otherStateLine, []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "unknown" })}, false},
		{"orphan", "NOT VERIFIED: verification budget unavailable: an orphaned Docker client is present", nil, true},
		{"verification", otherVerificationLine, []otherInspect{otherMemory(verify, 0)}, false},
		{"label", "NOT VERIFIED: verification budget unavailable: a verification container has no valid slot label", []otherInspect{descending}, false},
		{"registration", otherRegistrationLine, []otherInspect{otherMemory(dev, 2*otherUnit)}, false},
		{"unlimited", otherUnlimitedLine, []otherInspect{otherMemory(dev, 0)}, false},
		{"unregistered", otherUnregisteredLine, []otherInspect{otherFixture("unregistered", "", "running")}, false},
		{"duplicate", otherDuplicateLine, []otherInspect{verify, verify}, false},
		{"exclusive", otherExclusiveLine, []otherInspect{otherFixture("exclusive", "", "created")}, false},
	}
	for i, row := range rows {
		for _, paired := range []bool{false, true} {
			if paired && i == len(rows)-1 {
				continue
			}
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/paired=%v/reverse=%v", row.name, paired, reverse), func(t *testing.T) {
					list, orphan := slices.Clone(row.list), row.orphan
					if paired {
						list = append(list, rows[i+1].list...)
						orphan = orphan || rows[i+1].orphan
					}
					if reverse {
						slices.Reverse(list)
					}
					b, p := budgetCombinedFixture(t, list...)
					if orphan {
						p.procs = []procInfo{budgetOrphanFixture(b.locks.uid)}
					}
					r, cause := b.budgetReserve(context.Background(), budgetSingle)
					if r.release != nil {
						r.release()
						t.Fatal("refusal granted a reservation")
					}
					if got := cause.line("verification budget"); got != row.line {
						t.Fatalf("precedence line = %q; want %q", got, row.line)
					}
				})
			}
		}
	}
	// Listing and process failures share the highest rank even with hook evidence.
	for _, processFailure := range []bool{false, true} {
		b, p := budgetCombinedFixture(t, otherMemory(verify, 0))
		if processFailure {
			p.err = errors.New("private process error")
		} else {
			b.inventory.(*budgetFakeInventory).err = errors.New("private listing error")
		}
		budgetAssertCause(t, b, string(budgetStateUnknown))
	}
}

func TestBudgetCombinedM2Lines(t *testing.T) {
	one := otherFixture("verification", "0", "running")
	two := otherFixture("verification", "0,2", "running")
	for _, row := range []struct {
		name string
		c    otherInspect
	}{
		{"memory zero", otherMemory(one, 0)},
		{"memory one GiB", otherMemory(one, otherUnit/2)},
		{"memory three GiB", otherMemory(one, 3*otherUnit/2)},
		{"two slots two GiB", otherMemory(two, otherUnit)},
		{"two slots six GiB", otherMemory(two, 3*otherUnit)},
		{"swap unlimited", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap = -1 })},
		{"swap larger", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap++ })},
		{"swap smaller", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap-- })},
		{"two without class", otherLabel(two, otherClassLabel, "")},
		{"class with one", otherLabel(one, otherClassLabel, "browser")},
		{"three slots", otherLabel(two, otherSlotLabel, "0,1,2")},
		{"another image", otherEdit(one, func(c *otherInspect) { c.Config.Image = "another" })},
		{"wrong owner", otherLabel(one, containerOwnerLabel, "other")},
		{"wrong name", otherEdit(one, func(c *otherInspect) { c.Name = "/foreign" })},
		{"missing owner", otherEdit(one, func(c *otherInspect) { delete(c.Config.Labels, containerOwnerLabel) })},
		{"missing slot", otherLabel(one, otherSlotLabel, "")},
		{"unknown class", otherLabel(one, otherClassLabel, "other")},
	} {
		t.Run(row.name, func(t *testing.T) {
			b, _ := budgetCombinedFixture(t, row.c)
			budgetAssertCause(t, b, string(budgetOtherResult(otherVerificationLine)))
		})
	}
}

func TestBudgetCombinedHeldSlots(t *testing.T) {
	verify := otherFixture("verification", "1", "exited")
	dev := otherFixture("dev", "2", "running")
	for _, row := range []struct {
		name        string
		list        []otherInspect
		label, line string
	}{
		{"verification counted once", []otherInspect{verify}, "0", ""},
		{"verification and dev", []otherInspect{verify, otherLabel(dev, otherSlotLabel, "0")}, "2", ""},
		{"browser counted once", []otherInspect{otherFixture("verification", "0,2", "dead")}, "1", ""},
		{"dev overlaps verification", []otherInspect{verify, otherLabel(dev, otherSlotLabel, "1")}, "", otherDuplicateLine},
		{"verification overlaps verification", []otherInspect{verify, verify}, "", otherDuplicateLine},
		{"dev overlaps dev", []otherInspect{dev, dev}, "", otherDuplicateLine},
		{"dev holds slot", []otherInspect{otherLabel(dev, otherSlotLabel, "0")}, "1", ""},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%v", row.name, reverse), func(t *testing.T) {
				list := slices.Clone(row.list)
				if reverse {
					slices.Reverse(list)
				}
				b, _ := budgetCombinedFixture(t, list...)
				if row.line != "" {
					budgetAssertCause(t, b, strings.TrimPrefix(row.line, "NOT VERIFIED: verification budget "))
				} else {
					budgetAssertReserve(t, b, budgetSingle, row.label).release()
				}
			})
		}
	}
}
