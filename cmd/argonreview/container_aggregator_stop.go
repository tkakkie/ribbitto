package main

// aggregatorStopReason records why interpretation or completion was refused.
// The sentinel makes additions exercise both classifications in the stop test.
type aggregatorStopReason int

const (
	stopNone aggregatorStopReason = iota
	stopCap
	stopMalformed
	stopUnknownAction
	stopWithoutPackage
	stopOutsidePackage
	stopInvalidStart
	stopWithoutTest
	stopDuplicateTest
	stopOutsideTest
	stopWithoutRun
	stopDeadline
	stopParentCancellation
	stopKill
	stopTruncated
	stopMissingPackage
	stopMissingTest
	stopIncompleteRun
	stopRunnerError
	stopReasonCount
)

// String returns the recorded stop diagnostic for reports and log readers.
func (reason aggregatorStopReason) String() string {
	switch reason {
	case stopNone:
		return ""
	case stopCap:
		return "cap"
	case stopMalformed:
		return "malformed test stream"
	case stopUnknownAction:
		return "unknown test action"
	case stopWithoutPackage:
		return "event without package"
	case stopOutsidePackage:
		return "event outside a running package"
	case stopInvalidStart:
		return "duplicate or invalid package start"
	case stopWithoutTest:
		return "test without name"
	case stopDuplicateTest:
		return "duplicate test"
	case stopOutsideTest:
		return "event outside a running test"
	case stopWithoutRun:
		return "result without a running test"
	case stopDeadline:
		return "deadline"
	case stopParentCancellation:
		return "parent cancellation"
	case stopKill:
		return "kill"
	case stopTruncated:
		return "empty or truncated test stream"
	case stopMissingPackage:
		return "missing package result"
	case stopMissingTest:
		return "missing test result"
	case stopIncompleteRun:
		return "incomplete run"
	case stopRunnerError:
		return "runner error"
	default:
		panic("unclassified aggregator stop reason")
	}
}

// malformed identifies E5 reasons, which precede exit and completion checks.
func (reason aggregatorStopReason) malformed() bool {
	switch reason {
	case stopMalformed, stopUnknownAction, stopWithoutPackage, stopOutsidePackage,
		stopInvalidStart, stopWithoutTest, stopDuplicateTest, stopOutsideTest, stopWithoutRun:
		return true
	case stopNone, stopCap, stopDeadline, stopParentCancellation, stopKill,
		stopTruncated, stopMissingPackage, stopMissingTest, stopIncompleteRun, stopRunnerError:
		return false
	default:
		panic("unclassified aggregator stop precedence")
	}
}
