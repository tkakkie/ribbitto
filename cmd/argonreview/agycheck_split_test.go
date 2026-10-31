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
			wantAccounting := agyCheckSummaryText(o.Summary)
			if o.Outcome != "" {
				wantAccounting += "; " + tc.reason
			}
			if a.OK != tc.accounting || p.OK != tc.sequential || a.Observed != wantAccounting || p.Observed != tc.reason {
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

func TestAgyCheckTaskOnEither(t *testing.T) {
	for _, state := range []string{"DONE", "ERROR"} {
		t.Run(state, func(t *testing.T) {
			outcome := "allowed"
			if state == "ERROR" {
				outcome = "denied"
			}
			for _, tc := range []struct {
				name, reason string
				change       func(*agyCheckEvidence)
			}{
				{"complete", outcome, func(*agyCheckEvidence) {}},
				{"no-step", "no single intended step", func(e *agyCheckEvidence) {
					e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestResult)
				}},
				{"multiple-steps", "no single intended step", func(e *agyCheckEvidence) {
					e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, "manage_task", state) + agyCheckTestStep(1, "manage_task", state) + agyCheckTestResult)
				}},
				{"wrong-tool", "no single intended step", func(e *agyCheckEvidence) {
					e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, "write_file", state) + agyCheckTestResult)
				}},
				{"malformed", agyCheckTranscriptUnparsed, func(e *agyCheckEvidence) { e.Input.Transcript = []byte("{\n") }},
				{"missing-claim", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims = nil }},
				{"deny-claim", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims[0].Word = "deny" }},
				{"silent-claim", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims[0].Word = "silent" }},
				{"wrong-tag", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims[0].Tag = "task-off" }},
				{"duplicate-claim", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims = append(e.Claims, e.Claims[0]) }},
				{"extra-claim", "invalid hook claim", func(e *agyCheckEvidence) { e.Claims = append(e.Claims, agyCheckClaim{2, "allow", "task-on"}) }},
				{"probe", "unexpected side effect", func(e *agyCheckEvidence) { e.Probe = true }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					row, s, e := agyCheckEvidenceFixture(t, "task-on")
					e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, "manage_task", state) + agyCheckTestResult)
					tc.change(&e)
					observation := agyCheckEvidenceObservation(row, s, e)
					got := agyCheckClassify(observation, 4)
					if observation.Outcome != tc.reason || got.Observed != tc.reason || got.OK != (tc.name == "complete") {
						t.Fatalf("evidence=%+v classification=%+v", observation, got)
					}
					if tc.name == "complete" {
						var out strings.Builder
						if err := agyCheckRender(&out, agyCheckResult{Rows: []agyCheckResultRow{got}}); err != nil {
							t.Fatal(err)
						}
						line := strings.Join(strings.Fields(strings.Split(out.String(), "\n")[1]), " ")
						if !strings.HasPrefix(line, "task-on either "+outcome+" ok ") {
							t.Fatalf("report lost real outcome: %s", line)
						}
					}
				})
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
