package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

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

func (b budgetDockerInventory) budgetQuery(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 || (args[0] != "ps" && args[0] != "inspect") {
		return nil, errors.New("container query refused")
	}
	ctx, cancel := context.WithTimeout(ctx, containerCleanupTimeout)
	defer cancel()
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
	var c struct {
		ID     string `json:"Id"`
		Name   string
		Config struct {
			Image  string
			Labels map[string]string
		}
		State      struct{ Status string }
		HostConfig struct{ Memory, MemorySwap int64 }
	}
	if json.Unmarshal(data, &c) != nil || c.ID != id {
		return budgetContainer{}, errors.New("invalid container inspection")
	}
	return budgetContainer{c.ID, c.Name, c.Config.Image, c.State.Status, c.Config.Labels, c.HostConfig.Memory, c.HostConfig.MemorySwap}, nil
}

func (b budgetDockerInventory) budgetList(ctx context.Context) ([]budgetContainer, error) {
	ids, err := b.budgetIDs(ctx)
	if err != nil {
		return nil, err
	}
	var containers []budgetContainer
	for _, id := range ids {
		c, err := b.budgetInspect(ctx, id)
		if err != nil {
			// Disappearance requires independent evidence, not inspect's diagnosis.
			again, listErr := b.budgetIDs(ctx)
			if listErr == nil && !slices.Contains(again, id) {
				continue
			}
			return nil, err
		}
		containers = append(containers, c)
	}
	return containers, nil
}

func budgetOrphan(processes processTable, uid int) (bool, error) {
	procs, err := processes.list()
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
