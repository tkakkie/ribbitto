package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// otherUnit is the reservation size shared with budgetSlots.
const otherUnit int64 = 2 << 30

// otherSlots fixes the VM budget without choosing or acquiring a slot.
const otherSlots = 3

// otherSlotLabel records reservations, including those left by dead runners.
const otherSlotLabel = "argonreview.slot"

// otherClassLabel distinguishes the two-unit browser reservation.
const otherClassLabel = "argonreview.class"

// otherBudgetLabel registers containers managed outside the verification runner.
const otherBudgetLabel = "argonreview.budget"

// otherUnavailable keeps reports independent of Docker-controlled strings.
const otherUnavailable = "NOT VERIFIED: verification budget unavailable: "

// otherStateLine covers discovery failures without exposing their contents.
const otherStateLine = otherUnavailable + "the container state could not be determined"

// otherVerificationLine refuses reservations that cannot be trusted.
const otherVerificationLine = otherUnavailable + "a verification container does not match its reservation"

// otherRegistrationLine refuses inconsistent external registrations.
const otherRegistrationLine = otherUnavailable + "a registered container does not match its registration"

// otherUnlimitedLine refuses an unlimited dev container.
const otherUnlimitedLine = otherUnavailable + "a container has no memory limit"

// otherUnregisteredLine prevents unknown memory use from being treated as free.
const otherUnregisteredLine = otherUnavailable + "an unregistered container is present"

// otherDuplicateLine refuses overlapping reservations.
const otherDuplicateLine = otherUnavailable + "a slot is claimed twice"

// otherExclusiveLine pauses new verification without touching the exclusive user.
const otherExclusiveLine = "NOT VERIFIED: verification budget paused: an exclusive container is present"

// otherDocker permits injection of a read-only Docker query transport.
type otherDocker func(args ...string) ([]byte, error)

// otherInspect stays private so reports cannot accidentally disclose Docker data.
type otherInspect struct {
	ID     string `json:"Id"`
	Name   string
	Config struct {
		Image  string
		Labels map[string]string
	}
	State      struct{ Status string }
	HostConfig struct{ Memory, MemorySwap int64 }
}

// otherContainers contains only fixed text, counts, and validated reservations.
// Counts include invalid members of each kind; unknown non-verification states
// have no counted-state evidence and contribute only the state failure.
type otherContainers struct {
	line                                       string
	verification, dev, exclusive, other, total int
	held                                       [otherSlots]bool
}

// otherReadOnly is mandatory even for injected transports: classification has
// no authority to change foreign containers, regardless of their registration.
func otherReadOnly(docker otherDocker, args ...string) ([]byte, error) {
	if len(args) == 0 || (args[0] != "ps" && args[0] != "inspect") {
		return nil, errors.New("container query refused")
	}
	data, err := docker(args...)
	if err != nil {
		// Transport errors can contain names, labels, or raw command output.
		return nil, errors.New("container query failed")
	}
	return data, nil
}

// otherIDs validates the complete listing before any inspection is attempted.
func otherIDs(docker otherDocker) ([]string, error) {
	data, err := otherReadOnly(docker, "ps", "-a", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !otherValidID(id) || seen[id] {
			return nil, errors.New("invalid container listing")
		}
		seen[id] = true
	}
	return ids, nil
}

// otherValidID excludes truncated IDs and option-like listing contents.
func otherValidID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// otherCompleteInspect distinguishes missing limits from explicit zero limits.
// Presence is decoded separately to keep classifier fixtures in Docker's shape.
func otherCompleteInspect(data []byte, id string) (otherInspect, bool) {
	var c otherInspect
	var required struct {
		Name       *string
		Config     *struct{}
		State      *struct{ Status string }
		HostConfig *struct{ Memory, MemorySwap *int64 }
	}
	if json.Unmarshal(data, &c) != nil || json.Unmarshal(data, &required) != nil || c.ID != id {
		return c, false
	}
	if required.Name == nil || c.Name == "" || required.Config == nil || required.State == nil || required.HostConfig == nil {
		return c, false
	}
	if required.State != nil && required.State.Status == "" {
		return c, false
	}
	if required.HostConfig != nil && (required.HostConfig.Memory == nil || required.HostConfig.MemorySwap == nil) {
		return c, false
	}
	return c, true
}

// otherPoll observes every container, including stopped verification leftovers.
func otherPoll(docker otherDocker) otherContainers {
	for attempt := 0; attempt < 3; attempt++ {
		ids, err := otherIDs(docker)
		if err != nil {
			return otherClassify(nil, err)
		}
		var list []otherInspect
		var failed []string
		for _, id := range ids {
			data, inspectErr := otherReadOnly(docker, "inspect", "--type", "container", "--format", "{{json .}}", id)
			if inspectErr != nil {
				failed = append(failed, id)
				continue
			}
			c, complete := otherCompleteInspect(data, id)
			if !complete {
				err = errors.New("invalid container inspection")
				continue
			}
			list = append(list, c)
		}
		// One fresh listing confirms disappearances and detects stale views.
		again, listErr := otherIDs(docker)
		if listErr != nil {
			return otherClassify(list, listErr)
		}
		for _, id := range failed {
			if otherContainsID(again, id) {
				err = errors.New("container inspection failed")
			}
		}
		stale := false
		for _, id := range again {
			if !otherContainsID(ids, id) {
				stale = true
			}
		}
		if !stale {
			return otherClassify(list, err)
		}
	}
	return otherClassify(nil, errors.New("unstable container listing"))
}

// otherContainsID keeps disappearance checks separate from classification.
func otherContainsID(ids []string, id string) bool {
	for _, found := range ids {
		if found == id {
			return true
		}
	}
	return false
}

// otherSlotLabelSlots accepts only the small canonical reservation grammar.
func otherSlotLabelSlots(label string) ([]int, bool) {
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
	if len(slots) == 2 && (label[1] != ',' || slots[0] == slots[1]) {
		return nil, false
	}
	return slots, true
}

// otherVerificationConsistent checks each reservation rule independently so
// relaxing one cannot silently discard another check during future wiring.
func otherVerificationConsistent(c otherInspect, nameMatches bool, slots []int, valid bool) bool {
	if !nameMatches {
		return false
	}
	if c.Config.Labels[containerOwnerLabel] != containerOwnerValue {
		return false
	}
	if c.Config.Image != containerVerifyImage {
		return false
	}
	if !valid {
		return false
	}
	// Keep the reservation bound independent of the label parser.
	if len(slots) < 1 || len(slots) > 2 {
		return false
	}
	class := c.Config.Labels[otherClassLabel]
	if class != "" && class != "browser" {
		return false
	}
	if len(slots) == 2 && class != "browser" {
		return false
	}
	if class == "browser" && len(slots) != 2 {
		return false
	}
	if c.HostConfig.Memory != int64(len(slots))*otherUnit {
		return false
	}
	if c.HostConfig.MemorySwap != c.HostConfig.Memory {
		return false
	}
	return true
}

// otherCountedState keeps leftovers reserved even after their runner dies;
// foreign stopped containers consume no active budget. Unknown states fail closed.
func otherCountedState(status string, verification bool) (bool, bool) {
	switch status {
	case "created", "running", "paused", "restarting":
		return true, true
	case "exited", "dead", "removing":
		return verification, true
	default:
		return verification, false
	}
}

// otherClassify accumulates all evidence before selecting a line, so neither
// Docker order nor an exclusive container can hide an unavailable budget.
func otherClassify(list []otherInspect, listErr error) otherContainers {
	var result otherContainers
	var problems [7]bool
	problems[0] = listErr != nil
	for _, c := range list {
		nameMatches := strings.HasPrefix(c.Name, "/") && sweepContainerName.MatchString(strings.TrimPrefix(c.Name, "/"))
		_, ownerPresent := c.Config.Labels[containerOwnerLabel]
		verification := nameMatches || ownerPresent
		counted, known := otherCountedState(c.State.Status, verification)
		if !known || !otherValidID(c.ID) {
			problems[0] = true
		}
		if !counted {
			continue
		}
		result.total++
		slots, valid := otherSlotLabelSlots(c.Config.Labels[otherSlotLabel])
		switch {
		case verification:
			result.verification++
			if !otherVerificationConsistent(c, nameMatches, slots, valid) {
				problems[1] = true
				continue
			}
		case c.Config.Labels[otherBudgetLabel] == "dev":
			result.dev++
			if c.HostConfig.Memory == 0 {
				problems[3] = true
				continue
			}
			if !valid || c.HostConfig.Memory < 0 || c.HostConfig.MemorySwap != c.HostConfig.Memory || c.HostConfig.Memory > int64(len(slots))*otherUnit {
				problems[2] = true
				continue
			}
		case c.Config.Labels[otherBudgetLabel] == "exclusive":
			result.exclusive++
			problems[6] = true
			continue
		default:
			// Unknown registrations cannot establish a safe memory bound.
			result.other++
			problems[4] = true
			continue
		}
		for _, slot := range slots {
			if result.held[slot] {
				problems[5] = true
			}
			result.held[slot] = true
		}
	}
	// Registration mismatch wins the tie within the dev tier; all unavailable
	// causes precede exclusive pause, even if the exclusive was listed first.
	lines := [7]string{otherStateLine, otherVerificationLine, otherRegistrationLine, otherUnlimitedLine, otherUnregisteredLine, otherDuplicateLine, otherExclusiveLine}
	for i, problem := range problems {
		if problem {
			result.line = lines[i]
			break
		}
	}
	return result
}
