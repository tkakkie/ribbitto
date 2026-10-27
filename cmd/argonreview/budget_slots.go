package main

import (
	"context"
	"strconv"
	"strings"
)

const (
	budgetSlotLabel                  = "argonreview.slot"
	budgetUnit          int64        = otherUnit
	budgetStateUnknown  budgetResult = "unavailable: the container state could not be determined"
	budgetInvalidLabel  budgetResult = "unavailable: a verification container has no valid slot label"
	budgetDuplicate     budgetResult = "unavailable: a slot is claimed twice"
	budgetOrphanPresent budgetResult = "unavailable: an orphaned Docker client is present"
)

type budgetRequest int

const (
	budgetSingle budgetRequest = iota + 1
	budgetBrowserRequest
	budgetExclusive
)

type budgetReservation struct {
	slots   []int
	label   string
	memory  int64
	release func()
}

type budgetSlots struct {
	inventory budgetInventory
	processes processTable
	locks     budgetLocks
	// budgetOtherHook validates every reservation before trusting held slots.
	// Production uses this hook; tests can isolate reconciliation here.
	budgetHook func([]budgetContainer) ([3]bool, budgetResult)
}

func budgetParseSlots(label string) ([]int, bool) {
	if len(label) != 1 && len(label) != 3 {
		return nil, false
	}
	var slots []int
	for i := 0; i < len(label); i += 2 {
		if label[i] < '0' || label[i] > '2' {
			return nil, false
		}
		slots = append(slots, int(label[i]-'0'))
	}
	if len(slots) == 2 && (label[1] != ',' || slots[0] >= slots[1]) {
		return nil, false
	}
	return slots, true
}

func (b budgetSlots) budgetRead(ctx context.Context) ([3]bool, budgetResult) {
	// Read processes first, as in containerSweep.run: the client's exit is the
	// cause and the container's existence the effect, so neither can be missed.
	orphan, processErr := budgetOrphan(b.processes, b.locks.uid)
	containers, err := b.inventory.budgetList(ctx)
	var held [3]bool
	var cause budgetResult
	if err != nil || processErr != nil {
		cause = budgetStateUnknown
	}
	if orphan {
		cause = budgetPrefer(cause, budgetOrphanPresent)
	}
	for _, c := range containers {
		switch c.State {
		case "created", "running", "paused", "restarting", "exited", "dead", "removing":
		default:
			cause = budgetPrefer(cause, budgetStateUnknown)
		}
		_, ownerPresent := c.Labels[containerOwnerLabel]
		nameMatches := strings.HasPrefix(c.Name, "/") && sweepContainerName.MatchString(strings.TrimPrefix(c.Name, "/"))
		if !nameMatches && !ownerPresent {
			continue
		}
		// Even exited, dead and removing leftovers outlive their runner's flock.
		slots, valid := budgetParseSlots(c.Labels[budgetSlotLabel])
		if !valid {
			cause = budgetPrefer(cause, budgetInvalidLabel)
			continue
		}
		for _, slot := range slots {
			if held[slot] {
				cause = budgetPrefer(cause, budgetDuplicate)
			}
			held[slot] = true
		}
	}
	if b.budgetHook != nil {
		additional, result := b.budgetHook(containers)
		cause = budgetPrefer(cause, result)
		for slot, occupied := range additional {
			if occupied && held[slot] {
				cause = budgetPrefer(cause, budgetDuplicate)
			}
			held[slot] = held[slot] || occupied
		}
	}
	return held, cause
}

// Enumerate in lowest-slot order; a locked candidate must not hide a free one.
func budgetPlans(held [3]bool, request budgetRequest) [][]int {
	var plans [][]int
	for first := 0; first < 3; first++ {
		if held[first] {
			continue
		}
		if request == budgetSingle {
			plans = append(plans, []int{first})
			continue
		}
		for second := first + 1; second < 3; second++ {
			if held[second] {
				continue
			}
			if request == budgetBrowserRequest {
				plans = append(plans, []int{first, second})
			} else if first == 0 && second == 1 && !held[2] {
				plans = append(plans, []int{0, 1, 2})
			}
		}
	}
	return plans
}

func (b budgetSlots) budgetReserve(ctx context.Context, request budgetRequest) (budgetReservation, budgetResult) {
	if request < budgetSingle || request > budgetExclusive || b.locks.limit < 0 || b.locks.poll <= 0 {
		return budgetReservation{}, budgetBadRequest
	}
	deadline := b.locks.now().Add(b.locks.limit)
	for {
		if ctx.Err() != nil {
			return budgetReservation{}, budgetCanceled
		}
		result := b.budgetAttempt(ctx, request)
		if ctx.Err() != nil {
			if result.reservation.release != nil {
				result.reservation.release()
			}
			return budgetReservation{}, budgetCanceled
		}
		if result.reservation.release != nil || result.cause == budgetUnsafeLock || result.cause == budgetCanceled || result.cause == budgetBadRequest {
			return result.reservation, result.cause
		}
		remaining := deadline.Sub(b.locks.now())
		if remaining <= 0 {
			return budgetReservation{}, result.cause
		}
		if b.locks.aboutToSleep != nil {
			b.locks.aboutToSleep()
		}
		if b.locks.sleep(ctx, min(b.locks.poll, remaining)) != nil || ctx.Err() != nil {
			return budgetReservation{}, budgetCanceled
		}
		if !b.locks.now().Before(deadline) {
			return budgetReservation{}, result.cause
		}
	}
}

type budgetAttemptResult struct {
	reservation budgetReservation
	cause       budgetResult
}

func (b budgetSlots) budgetAttempt(ctx context.Context, request budgetRequest) budgetAttemptResult {
	held, result := b.budgetRead(ctx)
	if result != "" {
		return budgetAttemptResult{cause: result}
	}
	for _, slots := range budgetPlans(held, request) {
		var locks []budgetLock
		if request != budgetSingle {
			locks = append(locks, budgetBrowser)
		}
		var labels []string
		for _, slot := range slots {
			locks = append(locks, budgetSlot0+budgetLock(slot))
			labels = append(labels, strconv.Itoa(slot))
		}
		attempt := b.locks
		attempt.limit = 0
		release, result := attempt.acquire(ctx, locks)
		if result == budgetBusy {
			continue
		}
		if result != "" {
			return budgetAttemptResult{cause: result}
		}
		held, result = b.budgetRead(ctx)
		for _, slot := range slots {
			if result == "" && held[slot] {
				result = budgetBusy
			}
		}
		if ctx.Err() != nil {
			result = budgetCanceled
		}
		if result == "" {
			return budgetAttemptResult{reservation: budgetReservation{slots, strings.Join(labels, ","), int64(len(slots)) * budgetUnit, release}}
		}
		release()
		if result == budgetBusy {
			continue
		}
		return budgetAttemptResult{cause: result}
	}
	return budgetAttemptResult{cause: budgetBusy}
}
