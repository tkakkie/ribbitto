package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func agyCheckEvidenceFixture(t *testing.T, id string) (agyCheckRow, agyCheckScenario, agyCheckEvidence) {
	t.Helper()
	row := agyCheckObserveFindRow(t, id)
	s, err := agyCheckScenarioFor(id)
	if err != nil {
		t.Fatal(err)
	}
	e := agyCheckEvidence{}
	state := "DONE"
	if row.Group == "hook" && s.Mode != "silent" || row.Group == "workspace" || row.Group == "settings" {
		state = "ERROR"
	}
	events := []map[string]any{{"event": "init"}}
	if row.Group == "reconciliation" {
		// Transcript order intentionally differs from claim order. Repeats belong
		// to the independent claims, never to the order DONE/ERROR is reported.
		for i, word := range []string{"allow", "deny", "allow", "deny"} {
			e.Claims = append(e.Claims, agyCheckClaim{i + 1, word, id})
		}
		for i, st := range []string{"DONE", "DONE", "ERROR", "ERROR"} {
			events = append(events, map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": i, "step_type": "tool", "tool_name": s.tool(), "state": st}})
		}
		e.Input.CreatedFiles = []string{"created-01", "created-03"}
	} else if row.Group == "discovery" {
		// A run record proves loading at this candidate.
		e.Claims = []agyCheckClaim{{1, "allow", id}}
		events = append(events, map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 0, "step_type": "tool", "tool_name": s.tool(), "state": "DONE"}})
	} else if id != "read-url" && id != "execute-url" {
		events = append(events, map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 0, "step_type": "tool", "tool_name": s.tool(), "state": state}})
		if row.Group == "workspace" {
			e.Claims = []agyCheckClaim{{1, "allow", id}, {2, "deny", id + "-home"}}
		} else if s.Mode != agyCheckMissingHook {
			e.Claims = []agyCheckClaim{{1, s.Mode, id}}
		}
		e.Probe = s.Mode == "silent" || s.Outside
		if s.Stub {
			e.Stub = []agyCheckStubRecord{{1, "start", "-"}, {2, "request", "initialize"}, {3, "response", "initialize"}}
			tag := ""
			if id == "list-resources" {
				tag = "list"
			}
			if id == "read-resource" {
				tag = "read"
			}
			if tag != "" {
				e.Stub = append(e.Stub, agyCheckStubRecord{4, "request", tag}, agyCheckStubRecord{5, "response", tag})
			}
		}
	}
	events = append(events, map[string]any{"event": "result", "result": map[string]any{}})
	for _, event := range events {
		b, _ := json.Marshal(event)
		e.Input.Transcript = append(e.Input.Transcript, append(b, '\n')...)
	}
	return row, s, e
}

func TestAgyCheckScenarios(t *testing.T) {
	seen := map[string]int{}
	for _, s := range agyCheckScenarios {
		seen[s.ID]++
	}
	if len(agyCheckScenarios) != 29 {
		t.Fatal("scenario count differs")
	}
	for _, row := range agyCheckExpected {
		if seen[row.ID] != 1 {
			t.Fatalf("scenario missing or duplicated: %s", row.ID)
		}
		delete(seen, row.ID)
		s, _ := agyCheckScenarioFor(row.ID)
		if s.tool() == "" {
			t.Fatal("missing target tool")
		}
	}
	if len(seen) != 0 {
		t.Fatal("unknown scenario")
	}
}

func TestAgyCheckClaimOrder(t *testing.T) {
	for _, id := range []string{"reconciliation", "hook-precedence", "hook-same-name-agent", "hook-same-name-agents"} {
		t.Run(id, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, id)
			slices.Reverse(e.Claims)
			observation := agyCheckEvidenceObservation(row, s, e)
			if row.Group == "reconciliation" && (observation.Outcome != agyCheckBatchesUnobservable || observation.Summary.HookInvocations != 4 || observation.Summary.Repeats != 0) {
				t.Fatalf("claim-number order lost: %+v", observation)
			}
			if got := agyCheckClassify(observation, 4); !got.OK && row.Group != "reconciliation" {
				t.Fatalf("claim-number order lost: %+v", got)
			}
		})
	}
}

func TestAgyCheckRowEvidence(t *testing.T) {
	for _, row := range agyCheckExpected {
		t.Run(row.ID+"/baseline", func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, row.ID)
			got := agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4)
			if row.Group == "reconciliation" && (got.OK || got.Observed != agyCheckBatchesUnobservable+"; steps=4 hooks=4 allows=2 denies=2 errors=2 files=2 repeats=0") {
				t.Fatalf("batches invented: %+v", got)
			}
			if !got.OK && row.Group != "reconciliation" {
				t.Fatalf("valid evidence differs: %+v", got)
			}
		})
	}
	cases := []struct {
		name, id string
		change   func(*agyCheckEvidence)
	}{
		{"deny-no-claim", "hook-decision", func(e *agyCheckEvidence) { e.Claims = nil }},
		{"deny-word", "hook-exit1", func(e *agyCheckEvidence) { e.Claims[0].Word = "allow" }},
		{"deny-tag", "hook-exit2", func(e *agyCheckEvidence) { e.Claims[0].Tag = "wrong" }},
		{"deny-probe", "hook-timeout", func(e *agyCheckEvidence) { e.Probe = true }},
		{"missing-claim", "hook-missing", func(e *agyCheckEvidence) { e.Claims = []agyCheckClaim{{1, "allow", "hook-missing"}} }},
		{"missing-probe", "hook-missing", func(e *agyCheckEvidence) { e.Probe = true }},
		{"silent-word", "hook-silent", func(e *agyCheckEvidence) { e.Claims[0].Word = "deny" }},
		{"silent-probe", "hook-silent", func(e *agyCheckEvidence) { e.Probe = false }},
		{"settings-no-claim", "command-on", func(e *agyCheckEvidence) { e.Claims = nil }},
		{"settings-word", "command-off", func(e *agyCheckEvidence) { e.Claims[0].Word = "deny" }},
		{"settings-tag", "command-on", func(e *agyCheckEvidence) { e.Claims[0].Tag = "wrong" }},
		{"settings-probe", "command-off", func(e *agyCheckEvidence) { e.Probe = true }},
		{"stub-start", "mcp-on", func(e *agyCheckEvidence) { e.Stub[0].Kind = "request" }},
		{"stub-response", "mcp-off", func(e *agyCheckEvidence) { e.Stub = append(e.Stub, agyCheckStubRecord{4, "response", "verify"}) }},
		{"resource-response", "list-resources", func(e *agyCheckEvidence) { e.Stub = e.Stub[:4] }},
		{"resource-request", "read-resource", func(e *agyCheckEvidence) { e.Stub[3].Kind = "response" }},
		{"resource-order", "read-resource", func(e *agyCheckEvidence) { e.Stub[3].Kind, e.Stub[4].Kind = e.Stub[4].Kind, e.Stub[3].Kind }},
		{"exposure-word", "task-on", func(e *agyCheckEvidence) { e.Claims[0].Word = "silent" }},
		{"exposure-tag", "outside-read", func(e *agyCheckEvidence) { e.Claims[0].Tag = "wrong" }},
		{"outside-probe", "outside-write", func(e *agyCheckEvidence) { e.Probe = false }},
		{"discovery-unrelated", "discovery-root", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), "write_file", "view_file"))
		}},
		{"discovery-loaded-no-claim", "discovery-agent", func(e *agyCheckEvidence) {
			e.Claims = nil
		}},
		{"discovery-wrong-tag", "discovery-root", func(e *agyCheckEvidence) { e.Claims = []agyCheckClaim{{1, "allow", "wrong"}} }},
		{"reconciliation-gap", "reconciliation", func(e *agyCheckEvidence) { e.Claims[3].N = 9 }},
		{"reconciliation-contradiction", "reconciliation", func(e *agyCheckEvidence) {
			for i := range e.Claims {
				if e.Claims[i].Word == "allow" {
					e.Claims[i].Word = "deny"
				} else {
					e.Claims[i].Word = "allow"
				}
			}
		}},
		{"reconciliation-tag", "reconciliation", func(e *agyCheckEvidence) { e.Claims[0].Tag = "wrong" }},
		{"reconciliation-extra-file", "reconciliation", func(e *agyCheckEvidence) { e.Input.CreatedFiles[0] = "unexpected" }},
		{"reconciliation-name", "reconciliation", func(e *agyCheckEvidence) { e.Input.CreatedFiles[0] = "created-1" }},
		{"reconciliation-malformed", "reconciliation", func(e *agyCheckEvidence) { e.Input.Transcript = []byte("bad\n") }},
		{"no-step-non-url", "task-off", func(e *agyCheckEvidence) { e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestResult) }},
		{"url-step", "read-url", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, "read_url", "ERROR") + agyCheckTestResult)
		}},
	}
	for _, id := range []string{"hook-precedence", "hook-same-name-agent", "hook-same-name-agents"} {
		for _, name := range []string{"missing-home", "missing-workspace", "word", "tag", "duplicate", "order-gap", "probe"} {
			cases = append(cases, struct {
				name, id string
				change   func(*agyCheckEvidence)
			}{id + "-" + name, id, func(e *agyCheckEvidence) {
				switch name {
				case "missing-home":
					e.Claims = e.Claims[:1]
				case "missing-workspace":
					e.Claims = e.Claims[1:]
				case "word":
					e.Claims[1].Word = "allow"
				case "tag":
					e.Claims[1].Tag = "wrong"
				case "duplicate":
					e.Claims = append(e.Claims, e.Claims[0])
				case "order-gap":
					e.Claims[1].N = 3
				case "probe":
					e.Probe = true
				}
			}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, tc.id)
			if !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4).OK && row.Group != "reconciliation" {
				t.Fatal("baseline differs")
			}
			tc.change(&e)
			observation := agyCheckEvidenceObservation(row, s, e)
			if row.Group == "reconciliation" {
				want := map[string]string{"reconciliation-gap": "invalid reconciliation records", "reconciliation-contradiction": "contradictory reconciliation records", "reconciliation-tag": "invalid reconciliation records", "reconciliation-extra-file": "unexpected created file", "reconciliation-name": "unexpected created file", "reconciliation-malformed": agyCheckTranscriptUnparsed}[tc.name]
				if observation.Outcome != want {
					t.Fatalf("reconciliation reason=%q want %q", observation.Outcome, want)
				}
			}
			if got := agyCheckClassify(observation, 4); got.OK {
				t.Fatalf("changed evidence accepted: %+v", got)
			}
		})
	}
}

func TestAgyCheckOutputBlocks(t *testing.T) {
	pin, obs := agyCheckFixtures()
	r := agyCheckEvaluate(pin, pin, obs, 30, "")
	for _, listing := range [][]byte{[]byte(strings.Repeat("a", 64) + "  ./fixture\n"), nil} {
		r.BuiltinInfo = agyCheckBuiltinInfo(listing)
		var out strings.Builder
		if err := agyCheckRender(&out, r); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		for _, constraint := range []string{"Ordinary-agent regression check", "not tamper resistance", "the production guard keeps no decision log", "a match never approves version trust"} {
			if !strings.Contains(text, constraint) {
				t.Fatalf("reconciliation constraint missing: %s", constraint)
			}
		}
		if agyCheckExitCode(r) != 0 {
			t.Fatal("information changed exit code")
		}
		info, table := strings.Index(text, "INFO builtin/"), strings.Index(text, "ID ")
		if info < 0 || table <= info || !strings.HasSuffix(text, agyCheckSuccessLine+"\n") {
			t.Fatal("information must precede table and final success line")
		}
	}
}

// Unfinished tool activity must remain visible even beside a valid intended result.
func TestAgyCheckUnfinishedActivity(t *testing.T) {
	for _, id := range []string{"read-url", "execute-url", "hook-decision", "hook-same-name-agent", "discovery-agent", "reconciliation"} {
		for _, only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/only-%t", id, only), func(t *testing.T) {
				row, s, e := agyCheckEvidenceFixture(t, id)
				transcript := string(e.Input.Transcript)
				if only {
					transcript = agyCheckTestInit + agyCheckTestResult
				}
				transcript = strings.Replace(transcript, agyCheckTestResult, agyCheckTestStep(999, "unrelated", "ACTIVE")+agyCheckTestResult, 1)
				e.Input.Transcript = []byte(transcript)
				want := "no single intended step"
				switch id {
				case "read-url", "execute-url":
					want = "unexpected tool activity"
				case "reconciliation":
					want = "unexpected tool"
				}
				got := agyCheckEvidenceObservation(row, s, e)
				if got.Outcome != want || agyCheckClassify(got, 4).OK {
					t.Fatalf("unfinished activity hidden: %+v", got)
				}
				if got := agyCheckObserveRow(row, e.Input); got.Outcome != "unfinished tool activity" || agyCheckClassify(got, 4).OK {
					t.Fatalf("adapter hid activity: %+v", got)
				}
			})
		}
	}
}

func TestAgyCheckUnobservableNeverPasses(t *testing.T) {
	for _, row := range agyCheckExpected {
		for _, text := range []string{agyCheckLoadUnobservable, agyCheckBatchesUnobservable} {
			// Even changing an expectation must not manufacture evidence.
			row.Expected = text
			got := agyCheckClassifyRow(row, agyCheckObservation{ID: row.ID, Outcome: text, Loaded: true, Ran: true, Summary: agyCheckSummary{30, 30, 15, 15, 15, 15, 2, 0}}, 30)
			want := text
			if text == agyCheckBatchesUnobservable {
				want += "; steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0"
			}
			if got.OK || got.Observed != want || strings.Contains(got.Observed, "batches=") {
				t.Fatalf("unobservable passed: %+v", got)
			}
		}
	}
}
