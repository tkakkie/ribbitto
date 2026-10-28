package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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
	var events []map[string]any
	if row.Group == "reconciliation" {
		// Transcript order intentionally differs from claim order. Repeats belong
		// to the independent claims, never to the order DONE/ERROR is reported.
		for i, word := range []string{"allow", "deny", "allow", "deny"} {
			e.Claims = append(e.Claims, agyCheckClaim{i + 1, word, id})
		}
		for i, st := range []string{"DONE", "DONE", "ERROR", "ERROR"} {
			events = append(events, map[string]any{"event": "tool_step", "tool": s.tool(), "state": st, "batch": fmt.Sprint(i / 2)})
		}
		e.Input.CreatedFiles = []string{"created-01", "created-03"}
	} else if row.Group == "discovery" {
		// Use an unloaded candidate so unrelated evidence alone cannot pass.
		events = append(events, map[string]any{"event": "tool_step", "tool": s.tool(), "state": "DONE"})
	} else if id != "read-url" && id != "execute-url" {
		events = append(events, map[string]any{"event": "tool_step", "tool": s.tool(), "state": state})
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
	events = append(events, map[string]any{"event": "result"})
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
			if got := agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4); !got.OK {
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
			if !got.OK {
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
			e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), "write_to_file", "view_file"))
		}},
		{"discovery-loaded-no-claim", "discovery-agent", func(e *agyCheckEvidence) {
			e.Input.Transcript = append([]byte("{\"event\":\"hook_loaded\",\"path\":\".agent/hooks.json\"}\n"), e.Input.Transcript...)
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
		{"reconciliation-batches", "reconciliation", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), `"batch":"1"`, `"batch":"0"`))
		}},
		{"reconciliation-malformed", "reconciliation", func(e *agyCheckEvidence) { e.Input.Transcript = []byte("bad\n") }},
		{"no-step-non-url", "task-off", func(e *agyCheckEvidence) { e.Input.Transcript = []byte("{\"event\":\"result\"}\n") }},
		{"url-step", "read-url", func(e *agyCheckEvidence) {
			e.Input.Transcript = append([]byte("{\"event\":\"tool_step\",\"tool\":\"read_url\",\"state\":\"ERROR\"}\n"), e.Input.Transcript...)
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
			if !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4).OK {
				t.Fatal("baseline differs")
			}
			tc.change(&e)
			if got := agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4); got.OK {
				t.Fatalf("changed evidence accepted: %+v", got)
			}
		})
	}
}

func TestAgyCheckOutputBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		code       int
		extra      int
	}{
		{"none", "", 0, 0}, {"protected", ".AGENT/extra/hooks.json", 0, 0}, {"unprotected", "elsewhere/config.json", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin, obs := agyCheckFixtures()
			// Unloaded known candidates keep their coverage independent of this
			// test: only the extra location can change the result's exit status.
			for i, row := range agyCheckExpected {
				if row.Group == "discovery" {
					obs[i].Loaded, obs[i].Ran = false, false
				}
			}
			paths := []string{}
			if tc.path != "" {
				paths = append(paths, tc.path)
			}
			baseline := agyCheckEvaluate(pin, pin, obs, 30, "")
			r := agyCheckEvaluate(pin, pin, obs, 30, "", paths...)
			if !slices.Equal(r.Rows[:29], baseline.Rows) || len(r.Rows) != 29+tc.extra || agyCheckExitCode(r) != tc.code {
				t.Fatal("unlisted load changed table or lost failure")
			}
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
				if agyCheckExitCode(r) != tc.code {
					t.Fatal("information changed exit code")
				}
				info, table := strings.Index(text, "INFO builtin/"), strings.Index(text, "ID ")
				if info < 0 || table <= info {
					t.Fatal("information must precede table and final line")
				}
				if tc.path != "" && (strings.Index(text, "LOADED BUT UNLISTED") != 0 || strings.Index(text, tc.path) > info) {
					t.Fatal("unlisted block must precede information")
				}
				if tc.code == 0 && !strings.HasSuffix(text, agyCheckSuccessLine+"\n") {
					t.Fatal("success line is not last")
				}
				if tc.code != 0 && strings.Contains(text, agyCheckSuccessLine) {
					t.Fatal("unprotected load printed success")
				}
			}
		})
	}
}

func TestAgyCheckURLClaimsIgnored(t *testing.T) {
	for _, id := range []string{"read-url", "execute-url", "task-off"} {
		t.Run(id, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			defer func() {
				if err := f.cleanup(m.Isolation, r.Run); err != nil {
					t.Error(err)
				}
			}()
			agyCheckTestWrite(t, filepath.Join(r.Row, "out.jsonl"), []byte("{\"event\":\"result\"}\n"), 0o600)
			agyCheckTestWrite(t, filepath.Join(r.State, "calls/000001"), []byte("malformed record\n"), 0o600)
			s, _ := agyCheckScenarioFor(id)
			e, diagnostic, err := agyCheckCollect(m.Isolation, r, s, f)
			if err != nil {
				t.Fatal(err)
			}
			url := id == "read-url" || id == "execute-url"
			if (diagnostic == "") != url {
				t.Fatalf("URL claim exception widened or lost: diagnostic=%q", diagnostic)
			}
			if url && !agyCheckClassify(agyCheckEvidenceObservation(agyCheckObserveFindRow(t, id), s, e), 30).OK {
				t.Fatal("URL claims changed observation")
			}
		})
	}
}
