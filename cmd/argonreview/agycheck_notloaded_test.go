package main

import (
	"fmt"
	"strings"
	"testing"
)

// All fixtures here are synthetic, never evidence of agy's behaviour.
func TestAgyCheckDiscoveryNotLoadedEvidence(t *testing.T) {
	for _, id := range []string{"discovery-gemini-config", "discovery-gemini", "discovery-antigravity", "discovery-root", "discovery-agent"} {
		for _, name := range []string{"baseline", "zero", "two", "ERROR", "ACTIVE", "wrong-tool", "wrong-tag", "deny", "two-claims", "duplicate", "malformed-claim", "stub", "malformed", "opaque", "opaque-ERROR"} {
			t.Run(id+"/"+name, func(t *testing.T) {
				row, s, e := agyCheckEvidenceFixture(t, id)
				e.Claims = nil
				step := agyCheckTestStep(0, s.tool(), "DONE")
				switch name {
				case "zero":
					step = ""
				case "two":
					step += agyCheckTestStep(1, s.tool(), "DONE")
				case "ERROR", "ACTIVE":
					step = agyCheckTestStep(0, s.tool(), name)
				case "wrong-tool":
					step = agyCheckTestStep(0, "view_file", "DONE")
				case "wrong-tag":
					e.Claims = []agyCheckClaim{{1, "allow", "wrong"}}
				case "deny":
					e.Claims = []agyCheckClaim{{1, "deny", id}}
				case "two-claims", "duplicate":
					e.Claims = []agyCheckClaim{{1, "allow", id}, {2, "allow", id}}
					if name == "duplicate" {
						e.Claims[1] = e.Claims[0]
					}
				case "malformed-claim":
					e.Claims = []agyCheckClaim{{0, "invalid", id}}
				case "stub":
					e.Stub = []agyCheckStubRecord{{1, "start", "-"}}
				case "opaque", "opaque-ERROR":
					if name == "opaque-ERROR" {
						step = agyCheckTestStep(0, s.tool(), "ERROR")
					}
					step = strings.Replace(step, `"step_update":{`, `"text":"hook not loaded; deny","step_update":{`, 1)
				}
				e.Input.Transcript = []byte(agyCheckTestInit + step + agyCheckTestResult)
				if name == "malformed" {
					e.Input.Transcript = []byte("bad\n")
				}
				o := agyCheckEvidenceObservation(row, s, e)
				want := name == "baseline" || name == "opaque"
				if parsed, err := agyCheckTranscriptParse(e.Input.Transcript); err == nil && agyCheckNotLoaded(parsed, s, e) != want {
					t.Fatalf("negative evidence helper accepted %s", name)
				}
				if o.NotLoaded != want || o.DenyShown || agyCheckClassify(o, 4).OK || want && o.Outcome != agyCheckLoadUnobservable {
					t.Fatalf("negative evidence=%+v want NotLoaded=%t", o, want)
				}
			})
		}
	}
}

func TestAgyCheckDenyShownEvidence(t *testing.T) {
	for _, row := range agyCheckExpected {
		for _, change := range []string{"baseline", "wrong-tag", "DONE", "extra-claim", "probe"} {
			t.Run(row.ID+"/"+change, func(t *testing.T) {
				row, s, e := agyCheckEvidenceFixture(t, row.ID)
				switch change {
				case "wrong-tag":
					if len(e.Claims) > 0 {
						e.Claims[0].Tag = "wrong"
					}
				case "DONE":
					e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), "ERROR", "DONE"))
				case "extra-claim":
					e.Claims = append(e.Claims, agyCheckClaim{1, "deny", row.ID})
				case "probe":
					e.Probe = true
				}
				want := change == "baseline" && (row.ID == "hook-decision" || row.Group == "workspace")
				if o := agyCheckEvidenceObservation(row, s, e); o.DenyShown != want {
					t.Fatalf("deny evidence=%+v want %t", o, want)
				}
			})
		}
	}
}

func TestAgyCheckEvaluateDiscoveryNotLoaded(t *testing.T) {
	for _, name := range []string{"baseline", "ERROR", "two", "claim", "wrong-tag", "no-flag", "not-denied", "no-mechanism", "duplicate-mechanism", "discovery-mechanism", "reconciliation-mechanism", "single-row", "duplicate-discovery", "sequential", "malformed-records", "duplicate-records"} {
		t.Run(name, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, "discovery-root")
			e.Claims = nil
			switch name {
			case "ERROR":
				e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), "DONE", "ERROR"))
			case "two":
				e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, s.tool(), "DONE") + agyCheckTestStep(1, s.tool(), "DONE") + agyCheckTestResult)
			case "claim", "wrong-tag":
				e.Claims = []agyCheckClaim{{1, "deny", row.ID}}
				if name == "wrong-tag" {
					e.Claims[0] = agyCheckClaim{1, "allow", "wrong"}
				}
			case "sequential", "malformed-records", "duplicate-records":
				_, _, e = agyCheckSyntheticParallel(t, 2, 2)
				if name == "sequential" {
					e.Events = nil
					for i, c := range e.Claims {
						e.Events = append(e.Events, agyCheckHookEvent{2*i + 1, c.N, "start", c.Word, c.Tag}, agyCheckHookEvent{2*i + 2, c.N, "end", "", ""})
					}
				} else if name == "malformed-records" {
					e.Claims[0].N = 99
				} else {
					e.Claims = append(e.Claims, e.Claims[0])
				}
			}
			discovery := agyCheckEvidenceObservation(row, s, e)
			denyRow, denyScenario, denyEvidence := agyCheckEvidenceFixture(t, "hook-decision")
			mechanism := agyCheckEvidenceObservation(denyRow, denyScenario, denyEvidence)
			switch name {
			case "no-flag":
				mechanism.DenyShown = false
			case "not-denied":
				mechanism.Outcome = "allowed"
			case "discovery-mechanism":
				mechanism.ID = "discovery-agent"
			case "reconciliation-mechanism":
				mechanism.ID = "reconciliation"
			}
			pin, observations := agyCheckFixtures()
			for i := range observations {
				if observations[i].ID == row.ID {
					observations[i] = discovery
				}
				if observations[i].ID == "hook-decision" {
					observations[i] = mechanism
				}
			}
			if name == "no-mechanism" {
				observations = observations[1:]
			}
			if name == "duplicate-mechanism" {
				observations = append(observations, mechanism)
			}
			if name == "duplicate-discovery" {
				observations = append(observations, discovery)
			}
			if name == "single-row" {
				observations = []agyCheckObservation{discovery}
			}
			r := agyCheckEvaluate(pin, pin, observations, 30, "")
			want := name == "baseline"
			for _, got := range r.Rows {
				if got.ID == row.ID && got.OK != want {
					t.Fatalf("discovery=%+v want OK=%t", got, want)
				}
			}
			if (agyCheckExitCode(r) == agyCheckSuccess) != want {
				t.Fatalf("exit=%d want success=%t", agyCheckExitCode(r), want)
			}
		})
	}
}

func TestAgyCheckNotLoadedNeverFromAdapter(t *testing.T) {
	inputs := map[string]agyCheckObserveInput{}
	for _, row := range agyCheckExpected {
		if row.Group != "discovery" {
			continue
		}
		_, _, e := agyCheckEvidenceFixture(t, row.ID)
		inputs[row.ID] = e.Input
		if o := agyCheckObserveRow(row, e.Input); o.NotLoaded || o.DenyShown || agyCheckClassify(o, 4).OK {
			t.Fatalf("adapter manufactured evidence: %+v", o)
		}
	}
	for _, o := range agyCheckObserveAll(inputs) {
		if o.NotLoaded || o.DenyShown || agyCheckClassify(o, 4).OK {
			t.Fatalf("all adapter manufactured evidence: %+v", o)
		}
	}
	row := agyCheckObserveFindRow(t, "discovery-root")
	o := agyCheckObservation{ID: row.ID, Outcome: agyCheckLoadUnobservable, NotLoaded: true, DenyShown: true}
	if agyCheckClassify(o, 4).OK {
		t.Fatal("classification alone accepted silence")
	}
	for _, run := range []agyCheckRunResult{{Code: 124}, {Stragglers: []string{"synthetic"}}, {Code: 124, Stragglers: []string{"synthetic"}}} {
		if got := agyCheckRunObservation(row, o, run); got.NotLoaded || got.DenyShown || agyCheckClassify(got, 4).OK {
			t.Fatalf("run failure kept flags: %+v", got)
		}
	}
}

func TestAgyCheckNotLoadedSyntheticNotRealEvidence(t *testing.T) {
	// A fully synthetic run proves wiring only, never evidence of agy's behaviour.
	var observations []agyCheckObservation
	for _, row := range agyCheckExpected {
		row, s, e := agyCheckEvidenceFixture(t, row.ID)
		if row.Group == "discovery" && row.ID != "discovery-agent" && row.ID != "discovery-agents" {
			e.Claims = nil
		}
		if row.Group == "reconciliation" {
			row, s, e = agyCheckSyntheticParallel(t, 2, 2)
		}
		observations = append(observations, agyCheckEvidenceObservation(row, s, e))
	}
	pin, _ := agyCheckFixtures()
	r := agyCheckEvaluate(pin, pin, observations, 4, "")
	if agyCheckExitCode(r) != 0 {
		t.Fatalf("synthetic wiring differs: %+v", r)
	}
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Rows {
		if row.Group != "discovery" {
			continue
		}
		loaded := row.ID == "discovery-agent" || row.ID == "discovery-agents"
		want := fmt.Sprintf("loaded=%t ran=%t covered=true", loaded, loaded)
		if !loaded {
			want += "; not loaded: one DONE step, no claim, deny shown working in this run"
		}
		if row.Observed != want || !strings.Contains(out.String(), want) {
			t.Fatalf("negative evidence hidden: %+v", row)
		}
	}
}
