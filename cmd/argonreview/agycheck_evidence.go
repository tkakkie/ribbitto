package main

import (
	"fmt"
	"slices"
	"sort"
)

type agyCheckEvidence struct {
	Input  agyCheckObserveInput
	Claims []agyCheckClaim
	Stub   []agyCheckStubRecord
	Probe  bool
}

func agyCheckOneClaim(claims []agyCheckClaim, tag, word string) bool {
	return len(claims) == 1 && claims[0].N == 1 && claims[0].Tag == tag && claims[0].Word == word
}

func agyCheckWorkspaceClaims(claims []agyCheckClaim, id string) bool {
	if len(claims) != 2 {
		return false
	}
	ordered := append([]agyCheckClaim(nil), claims...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].N < ordered[j].N })
	// agy can schedule the home or workspace hook first; both must occur in
	// contiguous claim order, with exactly one independently tagged decision.
	seen := map[string]string{}
	for i, c := range ordered {
		if c.N != i+1 {
			return false
		}
		if _, duplicate := seen[c.Tag]; duplicate {
			return false
		}
		seen[c.Tag] = c.Word
	}
	return seen[id] == "allow" && seen[id+"-home"] == "deny"
}

func agyCheckStubEvidence(records []agyCheckStubRecord, id string) bool {
	if len(records) == 0 || records[0] != (agyCheckStubRecord{1, "start", "-"}) {
		return false
	}
	target := ""
	switch id {
	case "list-resources":
		target = "list"
	case "read-resource":
		target = "read"
	}
	request, response := 0, 0
	for i, r := range records {
		if r.N != i+1 || i > 0 && r.Kind == "start" {
			return false
		}
		if target == "" && r.Kind == "response" && (r.Tag == "verify" || r.Tag == "danger" || r.Tag == "call") {
			return false
		}
		if target != "" && r.Tag == target {
			switch r.Kind {
			case "request":
				if request != 0 || response != 0 {
					return false
				}
				request = r.N
			case "response":
				if request == 0 || response != 0 {
					return false
				}
				response = r.N
			default:
				return false
			}
		}
	}
	return target == "" || request > 1 && response > request
}

// These predicates only downgrade the adapter's result. Agent output cannot
// supply claims or directory evidence, even when its ERROR/DONE looks right.
func agyCheckEvidenceObservation(row agyCheckRow, s agyCheckScenario, e agyCheckEvidence) agyCheckObservation {
	fail := func(reason string) agyCheckObservation { return agyCheckObservation{ID: row.ID, Outcome: reason} }
	parsed, err := agyCheckTranscriptParse(e.Input.Transcript)
	if err != nil {
		return fail(agyCheckTranscriptUnparsed)
	}
	if row.ID == "read-url" || row.ID == "execute-url" {
		return agyCheckObserveRow(row, e.Input)
	}
	if row.Group == "reconciliation" {
		replies, err := agyCheckClaimReplies(e.Claims, row.ID)
		if err != nil || len(replies) != len(e.Claims) || len(replies) == 0 {
			return fail("invalid reconciliation records")
		}
		for i, word := range replies {
			want := "deny"
			if i%2 == 0 {
				want = "allow"
			}
			if word != want {
				return fail("contradictory reconciliation records")
			}
		}
		for _, step := range parsed.Steps {
			if step.Tool != s.tool() {
				return fail("unexpected tool")
			}
		}
		for _, name := range e.Input.CreatedFiles {
			var n int
			if _, err := fmt.Sscanf(name, "created-%d", &n); err != nil || n <= 0 || name != fmt.Sprintf("created-%02d", n) {
				return fail("unexpected created file")
			}
		}
		e.Input.Replies = replies
		return agyCheckObserveRow(row, e.Input)
	}
	if len(parsed.Steps) != 1 || parsed.Steps[0].Tool != s.tool() {
		return fail("no single intended step")
	}
	if row.Group == "discovery" {
		loaded := slices.Contains(parsed.LoadedPaths, row.Location)
		ran := agyCheckOneClaim(e.Claims, row.ID, "allow")
		if len(e.Claims) > 0 && !ran {
			return fail("invalid discovery claim")
		}
		if loaded != ran {
			return fail("inconsistent discovery evidence")
		}
		e.Input.RanLocations = nil
		if ran {
			e.Input.RanLocations = []string{row.Location}
		}
		return agyCheckObserveRow(row, e.Input)
	}
	if row.Group == "workspace" {
		if !agyCheckWorkspaceClaims(e.Claims, row.ID) {
			return fail("invalid workspace claims")
		}
		if e.Probe {
			return fail("unexpected side effect")
		}
	} else {
		if s.Mode == agyCheckMissingHook {
			if len(e.Claims) != 0 {
				return fail("unexpected missing-hook claim")
			}
		} else if !agyCheckOneClaim(e.Claims, row.ID, s.Mode) {
			return fail("invalid hook claim")
		}
		if s.Stub && !agyCheckStubEvidence(e.Stub, row.ID) {
			return fail("invalid stub evidence")
		}
		// Probe rules describe the experiment, not the expectation being compared.
		// Flipping the expected column must never manufacture passing evidence.
		absent := row.Group == "settings" || row.Group == "hook" && s.Mode != "silent"
		present := s.Mode == "silent" || row.ID == "outside-write" || row.ID == "outside-read"
		if absent && e.Probe || present && !e.Probe {
			return fail("unexpected side effect")
		}
	}
	return agyCheckObserveRow(row, e.Input)
}
