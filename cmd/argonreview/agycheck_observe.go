package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// agyCheckObserveInput separates transcript claims from the hook's own record;
// B3 must collect each independently before calling these pure adapters.
type agyCheckObserveInput struct {
	Transcript                          []byte
	Replies, RanLocations, CreatedFiles []string
}

// agyCheckObserveDiscovery leaves coverage to the core's guard-owned list.
func agyCheckObserveDiscovery(row agyCheckRow, loadedPaths, ranLocations []string) agyCheckObservation {
	return agyCheckObservation{ID: row.ID, Loaded: slices.Contains(loadedPaths, row.Location), Ran: slices.Contains(ranLocations, row.Location)}
}

// agyCheckObserveUnlistedLoads supplies information only: a new location must
// not silently expand the expected table or affect existing rows.
func agyCheckObserveUnlistedLoads(loadedPaths []string) []string {
	known := make(map[string]bool)
	for _, row := range agyCheckExpected {
		if row.Group == "discovery" {
			known[row.Location] = true
		}
	}
	var extra []string
	for _, path := range loadedPaths {
		if !known[path] {
			extra = append(extra, path)
			known[path] = true
		}
	}
	slices.Sort(extra)
	return extra
}

// agyCheckObserveCreatedPrefix is an assumed fixture convention, not proof
// that a directory entry came from an allowed tool call.
const agyCheckObserveCreatedPrefix = "created-"

// agyCheckObserveNoStep distinguishes absent evidence from an unloaded hook.
const agyCheckObserveNoStep = "no step"

// agyCheckObserveSummary refuses ambiguous records instead of repairing totals;
// only the core decides whether the independently collected counts reconcile.
func agyCheckObserveSummary(steps []agyCheckTranscriptStep, replies, createdFiles []string) (agyCheckSummary, error) {
	s := agyCheckSummary{ToolSteps: len(steps), HookInvocations: len(replies), FilesCreated: len(createdFiles)}
	for _, step := range steps {
		switch step.State {
		case "ERROR":
			s.Errors++
		case "DONE":
		default:
			return agyCheckSummary{}, errors.New("invalid step state")
		}
	}
	for i, reply := range replies {
		switch reply {
		case "allow":
			s.Allows++
		case "deny":
			s.Denies++
		default:
			return agyCheckSummary{}, errors.New("invalid hook reply")
		}
		if i > 0 && replies[i-1] == reply {
			s.Repeats++
		}
	}
	seen := make(map[string]bool)
	for _, name := range createdFiles {
		if !strings.HasPrefix(name, agyCheckObserveCreatedPrefix) || strings.Contains(name, "/") || seen[name] {
			return agyCheckSummary{}, errors.New("invalid created-file name")
		}
		seen[name] = true
	}
	var err error
	s.ParallelBatches, err = agyCheckTranscriptParallelBatches(steps)
	if err != nil {
		return agyCheckSummary{}, err
	}
	return s, nil
}

// agyCheckObserveRow discards every partial fact on error so an empty discovery
// observation cannot accidentally count as an unloaded, passing location.
func agyCheckObserveRow(row agyCheckRow, in agyCheckObserveInput) agyCheckObservation {
	failed := agyCheckObservation{ID: row.ID, Outcome: agyCheckTranscriptUnparsed}
	transcript, err := agyCheckTranscriptParse(in.Transcript)
	if err != nil {
		return failed
	}
	switch row.Group {
	case "hook", "workspace", "settings", "exposure":
		// Workspace locations do not imply discovery semantics. B3 must check
		// both same-name hook claims in claim-number order and absent side effect
		// alongside the final deny; Replies and RanLocations are not that check.
		return agyCheckObservation{ID: row.ID, Outcome: agyCheckTranscriptOutcome(row, transcript.Steps)}
	case "discovery":
		// Hook execution is recorded only on a tool call, so no call proves nothing.
		if len(transcript.Steps) == 0 {
			return agyCheckObservation{ID: row.ID, Outcome: agyCheckObserveNoStep}
		}
		return agyCheckObserveDiscovery(row, transcript.LoadedPaths, in.RanLocations)
	case "reconciliation":
		summary, err := agyCheckObserveSummary(transcript.Steps, in.Replies, in.CreatedFiles)
		if err != nil {
			return failed
		}
		return agyCheckObservation{ID: row.ID, Summary: summary}
	default:
		return failed
	}
}

// agyCheckObserveAll uses table order, never map order, to make row reports
// reproducible even when collection finishes in a different order.
func agyCheckObserveAll(inputs map[string]agyCheckObserveInput) []agyCheckObservation {
	observations := make([]agyCheckObservation, 0, len(agyCheckExpected))
	known := make(map[string]bool, len(agyCheckExpected))
	for _, row := range agyCheckExpected {
		known[row.ID] = true
		in, exists := inputs[row.ID]
		if !exists {
			observations = append(observations, agyCheckObservation{ID: row.ID, Outcome: "missing input"})
			continue
		}
		observations = append(observations, agyCheckObserveRow(row, in))
	}
	var unknown []string
	for id := range inputs {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	for _, id := range unknown {
		observations = append(observations, agyCheckObservation{ID: id, Outcome: "unknown row"})
	}
	return observations
}

// agyCheckBuiltinMaxBytes matches implementcheck.go's implementRead(list, 1<<20,
// false) cap: information must never hash a list implement-check could not read.
const agyCheckBuiltinMaxBytes = 1 << 20

// agyCheckBuiltinInfo hashes exact validated listing bytes for human comparison.
// Keeping information as a string prevents it becoming a trusted result row.
func agyCheckBuiltinInfo(listing []byte) string {
	const invalid = "INFO builtin/ listing could not be hashed: invalid listing"
	if len(listing) > agyCheckBuiltinMaxBytes {
		return invalid
	}
	if _, err := implementBuiltinList(listing); err != nil {
		return invalid
	}
	return fmt.Sprintf("INFO builtin/ listing SHA-256 %x over %d bytes; information only, no trusted file is written; the maintainer decides", sha256.Sum256(listing), len(listing))
}
