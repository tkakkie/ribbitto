package main

type verifyLogClass string

const (
	verifyLogOK           verifyLogClass = "ok"
	verifyLogMalformed    verifyLogClass = "malformed"
	verifyLogNoRun        verifyLogClass = "no-run"
	verifyLogNotListed    verifyLogClass = "not-listed"
	verifyLogAmbiguous    verifyLogClass = "ambiguous"
	verifyLogRange        verifyLogClass = "range"
	verifyLogStreamDenied verifyLogClass = "stream-denied"
	verifyLogStderrDenied verifyLogClass = "stderr-denied"
	verifyLogTainted      verifyLogClass = "tainted"
	verifyLogBudget       verifyLogClass = "budget"
	verifyLogTooLarge     verifyLogClass = "too-large"
)

// One fixed message per refusal class. No caller can insert untrusted input.
func verifyLogRefusals() map[verifyLogClass]string {
	return map[verifyLogClass]string{
		verifyLogMalformed:    "invalid verify arguments",
		verifyLogNoRun:        "no such run in this session",
		verifyLogNotListed:    "not listed",
		verifyLogAmbiguous:    "ambiguous: use #N",
		verifyLogRange:        "start out of range",
		verifyLogStreamDenied: "stream not allowed for this run",
		verifyLogStderrDenied: "stderr not servable after runner failure",
		verifyLogTainted:      "tainted unit: output withheld",
		verifyLogBudget:       "spent budget",
		verifyLogTooLarge:     "response too large",
	}
}
