package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const budgetInventoryTimeout = 30 * time.Second

type budgetContainer struct {
	ID, Name, Image, State string
	Labels                 map[string]string
	Memory, MemorySwap     int64
}

type budgetInventory interface {
	budgetList(context.Context) ([]budgetContainer, error)
}

// Injection changes the transport, never the adapter's read-only authority.
// A production caller can supply dockerRunner with its existing fixed environment.
type budgetDockerInventory struct{ runner containerRunner }

// Malformed evidence stays unavailable even if a later listing omits the ID.
// Only transport failure can be explained by independently confirmed absence.
type budgetInspectionInvalid struct{}

func (budgetInspectionInvalid) Error() string { return "invalid container inspection" }

func (b budgetDockerInventory) budgetQuery(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 || (args[0] != "ps" && args[0] != "inspect") {
		return nil, errors.New("container query refused")
	}
	ctx, cancel := context.WithTimeout(ctx, containerCleanupTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, errors.New("container query failed")
	}
	output := containerOutput{cancel: cancel}
	code, err := b.runner.run(ctx, args, dockerEnvironment(), output.stdoutWriter(), output.stderrWriter())
	if err != nil || code != 0 || output.over || ctx.Err() != nil {
		// Runner errors and both output streams may contain Docker-controlled data.
		return nil, errors.New("container query failed")
	}
	return output.stdout.Bytes(), nil
}

func (b budgetDockerInventory) budgetIDs(ctx context.Context) ([]string, error) {
	data, err := b.budgetQuery(ctx, "ps", "-a", "-q", "--no-trunc")
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	ids := strings.Fields(string(data))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !sweepID.MatchString(id) || seen[id] {
			return nil, errors.New("invalid container listing")
		}
		seen[id] = true
	}
	return ids, nil
}

func (b budgetDockerInventory) budgetInspect(ctx context.Context, id string) (budgetContainer, error) {
	data, err := b.budgetQuery(ctx, "inspect", "--format", "{{json .}}", id)
	if err != nil {
		return budgetContainer{}, fmt.Errorf("inspecting container: %w", err)
	}
	// Reuse M2's presence checks: absent limits must never become explicit zero.
	c, complete := otherCompleteInspect(data, id)
	if !complete {
		return budgetContainer{}, budgetInspectionInvalid{}
	}
	return budgetContainer{c.ID, c.Name, c.Config.Image, c.State.Status, c.Config.Labels, c.HostConfig.Memory, c.HostConfig.MemorySwap}, nil
}

func (b budgetDockerInventory) budgetList(ctx context.Context) ([]budgetContainer, error) {
	// One deadline covers all calls, including disappearance confirmation and
	// retries of a stale listing; per-command limits alone grow with inventory.
	ctx, cancel := context.WithTimeout(ctx, budgetInventoryTimeout)
	defer cancel()
	// A snapshot retry cannot explain away malformed evidence by disappearance.
	var malformed error
	for attempt := 0; attempt < 3; attempt++ {
		ids, err := b.budgetIDs(ctx)
		if err != nil {
			return nil, err
		}
		var containers []budgetContainer
		var inspectionErr error
		failed := make(map[string]error)
		for _, id := range ids {
			c, inspectErr := b.budgetInspect(ctx, id)
			if ctx.Err() != nil {
				return nil, errors.New("container query failed")
			}
			if inspectErr != nil {
				var invalid budgetInspectionInvalid
				if errors.As(inspectErr, &invalid) {
					malformed = inspectErr
				} else {
					failed[id] = inspectErr
				}
			} else {
				containers = append(containers, c)
			}
		}
		// Independent evidence confirms disappearances, and a new arrival makes
		// the snapshot stale. Match otherPoll without polling Docker twice.
		again, err := b.budgetIDs(ctx)
		if err != nil {
			return nil, err
		}
		stale := false
		for _, id := range again {
			if err := failed[id]; err != nil {
				inspectionErr = err
			}
			stale = stale || !slices.Contains(ids, id)
		}
		if !stale {
			if malformed != nil {
				return nil, malformed
			}
			if inspectionErr != nil {
				return nil, inspectionErr
			}
			return containers, nil
		}
	}
	return nil, errors.New("unstable container listing")
}

func budgetOrphan(ctx context.Context, processes processTable, uid int) (bool, error) {
	// Production process discovery must share the reservation's deadline too.
	var procs []procInfo
	var err error
	if contextual, ok := processes.(interface {
		listContext(context.Context) ([]procInfo, error)
	}); ok {
		procs, err = contextual.listContext(ctx)
	} else {
		procs, err = processes.list()
	}
	if err != nil {
		return false, errors.New("process listing failed")
	}
	for _, p := range procs {
		if p.ppid == 1 && p.uid == uid && dockerClientName(p.command) != "" {
			return true, nil
		}
	}
	return false, nil
}
