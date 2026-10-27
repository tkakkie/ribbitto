package main

import "strings"

func budgetOtherResult(line string) budgetResult {
	if line == "" {
		return ""
	}
	cause, ok := strings.CutPrefix(line, "NOT VERIFIED: verification budget ")
	if !ok {
		return budgetStateUnknown
	}
	return budgetResult(cause)
}

func budgetPrefer(a, b budgetResult) budgetResult {
	// Trust the observation first, then identify the wrong container, then
	// slot arithmetic; pause only when nothing is unavailable. M2's own order
	// is a subsequence, so its highest cause retains all precedence evidence.
	ranks := [...]budgetResult{
		budgetStateUnknown, budgetOrphanPresent, budgetOtherResult(otherVerificationLine),
		budgetInvalidLabel, budgetOtherResult(otherRegistrationLine), budgetOtherResult(otherUnlimitedLine),
		budgetOtherResult(otherUnregisteredLine), budgetDuplicate, budgetOtherResult(otherExclusiveLine),
	}
	for _, rank := range ranks {
		if rank == a || rank == b {
			return rank
		}
	}
	if a != "" || b != "" {
		return budgetStateUnknown
	}
	return ""
}

// budgetOtherHook classifies the same complete snapshot used for reconciliation.
// M2 validates verification reservations; only foreign held slots are returned
// because budgetRead already holds verification slots, including leftovers.
func budgetOtherHook(containers []budgetContainer) ([3]bool, budgetResult) {
	var all, foreign []otherInspect
	for _, c := range containers {
		var item otherInspect
		item.ID, item.Name = c.ID, c.Name
		item.Config.Image, item.Config.Labels = c.Image, c.Labels
		item.State.Status = c.State
		item.HostConfig.Memory, item.HostConfig.MemorySwap = c.Memory, c.MemorySwap
		all = append(all, item)
		_, ownerPresent := c.Labels[containerOwnerLabel]
		nameMatches := strings.HasPrefix(c.Name, "/") && sweepContainerName.MatchString(strings.TrimPrefix(c.Name, "/"))
		if !ownerPresent && !nameMatches {
			foreign = append(foreign, item)
		}
	}
	classified := otherClassify(all, nil)
	return otherClassify(foreign, nil).held, budgetOtherResult(classified.line)
}
