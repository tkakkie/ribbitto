package main

// agyCheckNotLoaded requires a completed intended call and empty local records:
// transcript prose cannot turn silence into negative evidence.
func agyCheckNotLoaded(p agyCheckTranscriptResult, s agyCheckScenario, e agyCheckEvidence) bool {
	return len(p.Steps) == 1 && p.Steps[0].Tool == s.tool() && p.Steps[0].State == "DONE" &&
		p.Unfinished == 0 && len(e.Claims) == 0 && len(e.Stub) == 0
}

// agyCheckWithDenyShown runs only after record validation; other denial causes
// cannot demonstrate that the hook's deny decision worked.
func agyCheckWithDenyShown(row agyCheckRow, o agyCheckObservation, claims []agyCheckClaim) agyCheckObservation {
	if (row.Group == "hook" || row.Group == "workspace") && o.Outcome == "denied" {
		for _, c := range claims {
			if c.Word == "deny" {
				o.DenyShown = true
			}
		}
	}
	return o
}

// agyCheckDenyMechanism excludes duplicate observations and unrelated groups so
// only an independently validated deny row can support discovery silence.
func agyCheckDenyMechanism(counts map[string]int, byID map[string]agyCheckObservation) bool {
	for _, row := range agyCheckExpected {
		o := byID[row.ID]
		if (row.Group == "hook" || row.Group == "workspace") && row.Expected == "denied" &&
			counts[row.ID] == 1 && o.DenyShown && o.Outcome == "denied" {
			return true
		}
	}
	return false
}
