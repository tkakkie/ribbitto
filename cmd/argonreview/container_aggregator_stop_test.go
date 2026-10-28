package main

import "testing"

func TestAggregatorStopReasons(t *testing.T) {
	rows := []struct {
		reason    aggregatorStopReason
		text      string
		malformed bool
	}{
		{stopNone, "", false},
		{stopCap, "cap", false},
		{stopMalformed, "malformed test stream", true},
		{stopUnknownAction, "unknown test action", true},
		{stopWithoutPackage, "event without package", true},
		{stopOutsidePackage, "event outside a running package", true},
		{stopInvalidStart, "duplicate or invalid package start", true},
		{stopWithoutTest, "test without name", true},
		{stopDuplicateTest, "duplicate test", true},
		{stopOutsideTest, "event outside a running test", true},
		{stopWithoutRun, "result without a running test", true},
		{stopDeadline, "deadline", false},
		{stopParentCancellation, "parent cancellation", false},
		{stopKill, "kill", false},
		{stopTruncated, "empty or truncated test stream", false},
		{stopMissingPackage, "missing package result", false},
		{stopMissingTest, "missing test result", false},
		{stopIncompleteRun, "incomplete run", false},
		{stopRunnerError, "runner error", false},
	}
	if len(rows) != int(stopReasonCount) {
		t.Fatal("every stop constant needs an explicit classification assertion")
	}
	for i, tc := range rows {
		if int(tc.reason) != i || tc.reason.String() != tc.text || tc.reason.malformed() != tc.malformed {
			t.Fatalf("stop %d classification differs: %+v", i, tc)
		}
	}
}
