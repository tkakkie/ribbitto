package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

type budgetStartInventory interface {
	budgetInventory
	budgetInspect(context.Context, string) (budgetContainer, error)
}

// Mutation authority is deliberately separate from the read-only inventory.
type budgetStartDocker struct{ runner containerRunner }

func (b budgetStartDocker) mutate(ctx context.Context, args ...string) error {
	valid := len(args) == 2 && args[0] == "start" && otherValidID(args[1]) ||
		len(args) == 4 && args[0] == "stop" && args[1] == "--time" && args[2] == "2" && otherValidID(args[3])
	if !valid {
		return errors.New("container mutation refused")
	}
	ctx, cancel := context.WithTimeout(ctx, containerCleanupTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return errors.New("container mutation failed")
	}
	output := containerOutput{cancel: cancel}
	code, err := b.runner.run(ctx, args, dockerEnvironment(), output.stdoutWriter(), output.stderrWriter())
	if err != nil || code != 0 || output.over || ctx.Err() != nil {
		return errors.New("container mutation failed")
	}
	return nil
}

type budgetStart struct {
	inventory              budgetStartInventory
	processes              processTable
	locks                  budgetLocks
	runner                 budgetStartDocker
	devWait, exclusiveWait time.Duration
}

func newBudgetStart() budgetStart {
	runner := dockerRunner{}
	return budgetStart{budgetDockerInventory{runner}, psTable{}, newBudgetLocks(), budgetStartDocker{runner}, 2 * time.Minute, 15 * time.Minute}
}

func budgetStartArguments(args []string) bool {
	return len(args) == 2 && args[0] == "--container" && otherValidID(args[1])
}

func budgetStartCommand(args []string, stdout, stderr io.Writer) int {
	if !budgetStartArguments(args) {
		return budgetStartUsage(stderr)
	}
	info, ok := debug.ReadBuildInfo()
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) })
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "argonreview: budget-start trusted build check failed")
		return 1
	}
	return newBudgetStart().command(context.Background(), args, stdout, stderr)
}

func budgetStartUsage(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "usage: argonreview budget-start --container <full 64-hex ID>")
	return 2
}

func (b budgetStart) command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !budgetStartArguments(args) {
		return budgetStartUsage(stderr)
	}
	if check := b.start(ctx, args[1]); check != "" {
		_, _ = fmt.Fprintln(stderr, "argonreview: budget-start "+check)
		return 1
	}
	_, err := fmt.Fprintln(stdout, args[1])
	if err != nil {
		return 1
	}
	return 0
}

// The lock set is derived only from the inspected registration, never caller input.
func budgetStartLocks(c budgetContainer) ([]budgetLock, bool) {
	_, owner := c.Labels[containerOwnerLabel]
	if c.State != "created" || owner || sweepContainerName.MatchString(strings.TrimPrefix(c.Name, "/")) {
		return nil, false
	}
	if c.Labels[otherBudgetLabel] == "exclusive" {
		if _, present := c.Labels[budgetSlotLabel]; present {
			return nil, false
		}
		return []budgetLock{budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2}, true
	}
	slots, valid := budgetParseSlots(c.Labels[budgetSlotLabel])
	if c.Labels[otherBudgetLabel] != "dev" || !valid {
		return nil, false
	}
	// Reuse M2's limit/registration rules; M1b above adds canonical slot order.
	if _, cause := budgetOtherHook([]budgetContainer{c}); cause != "" {
		return nil, false
	}
	var locks []budgetLock
	for _, slot := range slots {
		locks = append(locks, budgetSlot0+budgetLock(slot))
	}
	return locks, true
}

func budgetStartSame(a, b budgetContainer, state string) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Image == b.Image && b.State == state &&
		maps.Equal(a.Labels, b.Labels) && a.Memory == b.Memory && a.MemorySwap == b.MemorySwap
}

// Fetch inside budgetRead so process discovery still precedes the container
// snapshot: the client's exit is the cause, and container creation the effect.
type budgetStartOthers struct {
	inventory budgetInventory
	id        string
	first     budgetContainer
	check     string
}

func (b *budgetStartOthers) budgetList(ctx context.Context) ([]budgetContainer, error) {
	snapshot, err := b.inventory.budgetList(ctx)
	if err != nil {
		b.check = "snapshot inspection failed"
		return nil, err
	}
	first := b.first
	var others []budgetContainer
	found := 0
	for _, c := range snapshot {
		if c.ID != b.id {
			others = append(others, c)
			continue
		}
		found++
		if !budgetStartSame(first, c, "created") {
			b.check = "target changed under locks"
			return nil, errors.New("target inspection failed")
		}
	}
	if found != 1 {
		b.check = "target missing or duplicated under locks"
		return nil, errors.New("target inspection failed")
	}
	return others, nil
}

func (b budgetStart) start(parent context.Context, id string) string {
	first, err := b.inventory.budgetInspect(parent, id)
	if err != nil || first.ID != id {
		return "initial inspection failed"
	}
	requested, valid := budgetStartLocks(first)
	if !valid {
		return "target registration check failed"
	}
	wait := b.devWait
	if first.Labels[otherBudgetLabel] == "exclusive" {
		wait = b.exclusiveWait
	}
	if wait <= 0 || b.locks.poll <= 0 {
		return "wait configuration check failed"
	}
	deadline := b.locks.now().Add(wait)
	ctx, cancel := context.WithTimeout(parent, wait)
	defer cancel()
	for {
		if ctx.Err() != nil || !b.locks.now().Before(deadline) {
			return "busy"
		}
		attempt := b.locks
		attempt.limit = 0
		release, cause := attempt.acquire(ctx, requested)
		if cause == budgetCanceled && parent.Err() == nil {
			return "busy"
		}
		if cause != "" && cause != budgetBusy {
			return string(cause)
		}
		if release != nil {
			check, conflict := b.attempt(ctx, id, first, requested, deadline)
			release()
			if !conflict {
				return check
			}
		}
		remaining := deadline.Sub(b.locks.now())
		if remaining <= 0 || ctx.Err() != nil {
			return "busy"
		}
		if b.locks.aboutToSleep != nil {
			b.locks.aboutToSleep()
		}
		if b.locks.sleep(ctx, min(b.locks.poll, remaining)) != nil {
			if parent.Err() == nil && ctx.Err() != nil {
				return "busy"
			}
			return "wait interrupted"
		}
	}
}

func (b budgetStart) attempt(ctx context.Context, id string, first budgetContainer, requested []budgetLock, deadline time.Time) (string, bool) {
	others := &budgetStartOthers{inventory: b.inventory, id: id, first: first}
	slots := budgetSlots{inventory: others, processes: b.processes, locks: b.locks, budgetHook: budgetOtherHook}
	held, cause := slots.budgetRead(ctx)
	if others.check != "" {
		return others.check, false
	}
	if cause != "" && cause != budgetOtherResult(otherExclusiveLine) {
		return string(cause), false
	}
	conflict := cause != ""
	for _, lock := range requested {
		if lock != budgetBrowser && held[lock-budgetSlot0] {
			conflict = true
		}
	}
	if conflict {
		return "busy", true
	}
	if ctx.Err() != nil || !b.locks.now().Before(deadline) {
		return "busy", false
	}
	startErr := b.runner.mutate(ctx, "start", id)
	// Inspect even after a failed start: Docker may have started it before the
	// transport failed. The wait deadline and caller cancellation must not prevent
	// inspection or cleanup of the exact ID this call may have started.
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerCleanupTimeout)
	after, inspectErr := b.inventory.budgetInspect(inspectCtx, id)
	cancel()
	if inspectErr != nil || !budgetStartSame(first, after, "running") {
		if err := b.runner.mutate(context.WithoutCancel(ctx), "stop", "--time", "2", id); err != nil {
			return "post-start inspection failed; stop failed", false
		}
		return "post-start inspection failed", false
	}
	if startErr != nil {
		if err := b.runner.mutate(context.WithoutCancel(ctx), "stop", "--time", "2", id); err != nil {
			return "start failed; stop failed", false
		}
		return "start failed", false
	}
	return "", false
}
