package main

import (
	"strings"
	"testing"
)

func TestAgyCheckSplitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		change                 func(*agyCheckEvidence)
		accounting, sequential bool
		reason                 string
	}{
		{"overlapping", func(*agyCheckEvidence) {}, true, false, "parallel: 2 overlapping batches"},
		{"sequential", func(e *agyCheckEvidence) {
			e.Events = nil
			for i, c := range e.Claims {
				e.Events = append(e.Events, agyCheckHookEvent{2*i + 1, c.N, "start", c.Word, c.Tag}, agyCheckHookEvent{2*i + 2, c.N, "end", "", ""})
			}
		}, true, true, "sequential"},
		{"extra-claim", func(e *agyCheckEvidence) { e.Claims = append(e.Claims, agyCheckClaim{5, "allow", "reconciliation"}) }, false, false, "wrong hook event count"},
		{"missing-error", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), "ERROR", "DONE", 1))
		}, false, false, "parallel: 2 overlapping batches"},
		{"extra-file", func(e *agyCheckEvidence) { e.Input.CreatedFiles = append(e.Input.CreatedFiles, "created-05") }, false, false, "parallel: 2 overlapping batches"},
		{"repeat", func(e *agyCheckEvidence) { e.Claims[1].Word = "allow" }, false, false, "contradictory reconciliation records"},
		{"missing", func(e *agyCheckEvidence) { e.Claims = nil }, false, false, "invalid reconciliation records"},
		{"wrong-tag", func(e *agyCheckEvidence) { e.Claims[0].Tag = "other" }, false, false, "invalid reconciliation records"},
		{"bad-file", func(e *agyCheckEvidence) { e.Input.CreatedFiles[0] = "created-00" }, false, false, "unexpected created file"},
		{"bad-transcript", func(e *agyCheckEvidence) { e.Input.Transcript = []byte("synthetic invalid\n") }, false, false, agyCheckTranscriptUnparsed},
		{"wrong-tool", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), "write_file", "view_file", 1))
		}, false, false, "unexpected tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, s, e := agyCheckSyntheticParallel(t, 2, 2)
			tc.change(&e)
			o := agyCheckEvidenceObservation(row, s, e)
			a, p := agyCheckClassify(o, 4), agyCheckClassify(agyCheckParallelObservation(o), 4)
			if a.OK != tc.accounting || p.OK != tc.sequential || a.Observed != agyCheckSummaryText(o.Summary) || p.Observed != tc.reason {
				t.Fatalf("accounting=%+v parallel=%+v", a, p)
			}
			if tc.name == "repeat" && (o.Summary.Repeats != 2 || o.Summary.Allows != 3) {
				t.Fatal("contradiction hid counts")
			}
			if tc.accounting {
				if agyCheckClassify(o, 5).OK || agyCheckClassify(agyCheckParallelObservation(o), 5).OK {
					t.Fatal("minimum not binding")
				}
			}
			for _, run := range []agyCheckRunResult{{Code: 124}, {Stragglers: []string{"synthetic"}}} {
				overridden := agyCheckRunObservation(row, o, run)
				if agyCheckClassify(overridden, 4).OK || agyCheckClassify(agyCheckParallelObservation(overridden), 4).OK {
					t.Fatal("override lost")
				}
			}
			flipped := p.agyCheckRow
			flipped.Expected = p.Observed
			if !tc.sequential && agyCheckClassifyRow(flipped, agyCheckParallelObservation(o), 4).OK {
				t.Fatal("flipped expectation manufactured evidence")
			}
		})
	}
}

func TestAgyCheckTaskOnDenied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*agyCheckEvidence)
		ok     bool
	}{
		{"denied", func(*agyCheckEvidence) {}, true},
		{"allowed", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), "ERROR", "DONE", 1))
		}, false},
		{"probe", func(e *agyCheckEvidence) { e.Probe = true }, false},
		{"missing-claim", func(e *agyCheckEvidence) { e.Claims = nil }, false},
		{"deny-claim", func(e *agyCheckEvidence) { e.Claims[0].Word = "deny" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, "task-on")
			tc.change(&e)
			if got := agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4); got.OK != tc.ok {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestAgyCheckParallelInvalidCounts(t *testing.T) {
	for _, change := range []func(*agyCheckSummary){
		func(s *agyCheckSummary) { s.HookInvocations++ },
		func(s *agyCheckSummary) { s.Errors-- },
		func(s *agyCheckSummary) { s.FilesCreated++ },
		func(s *agyCheckSummary) { s.Repeats++ },
	} {
		o := agyCheckObservation{ID: "parallel-batches", Outcome: "sequential", Summary: agyCheckSummary{ToolSteps: 4, HookInvocations: 4, Allows: 2, Denies: 2, Errors: 2, FilesCreated: 2}}
		change(&o.Summary)
		if got := agyCheckClassify(o, 4); got.OK || got.Observed != agyCheckSummaryText(o.Summary) {
			t.Fatalf("invalid accounting passed: %+v", got)
		}
	}
}
