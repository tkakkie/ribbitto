package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func agyCheck149Active(index int, tool string) string {
	return strings.TrimSuffix(agyCheckTestStep(index, tool, "ACTIVE"), "}}\n") +
		fmt.Sprintf(`,"tool_info":{"name":%q,"parameters":{"Path":"x"}}}}`, tool) + "\n"
}

func agyCheck149Unknown(index int) string {
	return fmt.Sprintf("{\"event\":\"step_update\",\"step_update\":{\"step_index\":%d,\"state\":\"DONE\",\"step_type\":\"unknown\",\"duration_seconds\":0.5}}\n", index)
}

func TestAgyCheck149UnknownAccept(t *testing.T) {
	for _, tool := range []string{"list_resources", "read_resource", "generic"} {
		for _, interleaved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/interleaved-%t", tool, interleaved), func(t *testing.T) {
				body := agyCheck149Active(0, tool)
				groups := []int{1}
				if interleaved {
					body += agyCheckTestResponse(1, "agent_response", "DONE")
					groups = []int{1, 0}
				}
				got, err := agyCheckTranscriptParse([]byte(agyCheckTestInit + body + agyCheck149Unknown(0) + agyCheckTestResult))
				want := agyCheckTranscriptResult{Steps: []agyCheckTranscriptStep{{tool, "DONE"}}, Groups: groups}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("facts=%+v want %+v: %v", got, want, err)
				}
			})
		}
	}
}

func TestAgyCheck149UnknownReject(t *testing.T) {
	const marker = "UNTRUSTED_TRANSCRIPT_MARKER"
	active := agyCheck149Active(0, marker)
	unknown := strings.TrimSuffix(agyCheck149Unknown(0), "}}\n") + `,"text_delta":"` + marker + "\"}}\n"
	cases := []struct{ name, body string }{
		{"first sighting", unknown},
		{"after user_input", agyCheckTestResponse(0, "user_input", "ACTIVE") + unknown},
		{"after agent_response", agyCheckTestResponse(0, "agent_response", "ACTIVE") + unknown},
		{"after user_input DONE", agyCheckTestResponse(0, "user_input", "DONE") + unknown},
		{"after agent_response DONE", agyCheckTestResponse(0, "agent_response", "DONE") + unknown},
		{"after tool DONE", agyCheckTestStep(0, marker, "DONE") + unknown},
		{"after tool ERROR", agyCheckTestStep(0, marker, "ERROR") + unknown},
		{"duplicate unknown", active + unknown + unknown},
		{"unknown ACTIVE", active + strings.Replace(unknown, `"DONE"`, `"ACTIVE"`, 1)},
		{"unknown ERROR", active + strings.Replace(unknown, `"DONE"`, `"ERROR"`, 1)},
		{"different index", active + strings.Replace(unknown, `"step_index":0`, `"step_index":1`, 1)},
	}
	for _, field := range []struct{ name, value string }{
		{"tool_name same", `"tool_name":"` + marker + `"`},
		{"tool_name other", `"tool_name":"other"`}, {"tool_name null", `"tool_name":null`},
		{"tool_info valid", `"tool_info":{"name":"` + marker + `","parameters":{}}`},
		{"tool_info null", `"tool_info":null`}, {"tool_info empty", `"tool_info":{}`},
	} {
		cases = append(cases, struct{ name, body string }{field.name, active + strings.TrimSuffix(unknown, "}}\n") + "," + field.value + "}}\n"})
	}
	for _, kind := range []string{"Unknown", "other", ""} {
		cases = append(cases, struct{ name, body string }{"type " + kind, active + strings.Replace(unknown, `"unknown"`, fmt.Sprintf("%q", kind), 1)})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckTranscriptParse([]byte(agyCheckTestInit + tc.body + agyCheckTestResult))
			if err == nil || !reflect.DeepEqual(got, agyCheckTranscriptResult{}) {
				t.Fatalf("invalid transcript accepted or partial facts retained: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("diagnostic echoed transcript: %v", err)
			}
		})
	}
}

func TestAgyCheck149ResourceEvidence(t *testing.T) {
	for _, id := range []string{"list-resources", "read-resource"} {
		for _, complete := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/complete-%t", id, complete), func(t *testing.T) {
				row, s, e := agyCheckEvidenceFixture(t, id)
				ordinary := agyCheckEvidenceObservation(row, s, e)
				lines := strings.SplitAfter(string(e.Input.Transcript), "\n")
				lines[1] = agyCheck149Active(0, s.tool())
				if complete {
					lines[1] += agyCheck149Unknown(0)
				}
				e.Input.Transcript = []byte(strings.Join(lines, ""))
				got := agyCheckEvidenceObservation(row, s, e)
				if agyCheckClassify(got, 4).OK != complete {
					t.Fatalf("completion classification: %+v", got)
				}
				if complete {
					if got != ordinary || got.Outcome != "answered" {
						t.Fatalf("observation=%+v want %+v answered", got, ordinary)
					}
				} else {
					parsed, err := agyCheckTranscriptParse(e.Input.Transcript)
					if err != nil || parsed.Unfinished != 1 || got.Outcome != "no single intended step" {
						t.Fatalf("unfinished activity hidden: %+v %+v %v", got, parsed, err)
					}
				}
			})
		}
	}
}

func TestAgyCheck149ReadFile(t *testing.T) {
	for _, tc := range []struct {
		name, id, body, outcome, evidence string
		ok                                bool
	}{
		{"read DONE", "outside-read", agyCheckTestStep(0, "read_file", "DONE"), "allowed", "allowed", true},
		{"read ERROR", "outside-read", agyCheckTestStep(0, "read_file", "ERROR"), "denied", "denied", false},
		{"view DONE", "outside-read", agyCheckTestStep(0, "view_file", "DONE"), "allowed", "allowed", true},
		{"both", "outside-read", agyCheckTestStep(0, "view_file", "DONE") + agyCheckTestStep(1, "read_file", "DONE"), "multiple steps", "no single intended step", false},
		{"outside-write", "outside-write", agyCheckTestStep(0, "read_file", "DONE"), "no step", "no single intended step", false},
		{"task-off", "task-off", agyCheckTestStep(0, "read_file", "DONE"), "no step", "no single intended step", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, tc.id)
			e.Input.Transcript = []byte(agyCheckTestInit + tc.body + agyCheckTestResult)
			parsed, err := agyCheckTranscriptParse(e.Input.Transcript)
			if err != nil || agyCheckTranscriptOutcome(row, parsed.Steps) != tc.outcome {
				t.Fatalf("tool outcome=%q want %q: %v", agyCheckTranscriptOutcome(row, parsed.Steps), tc.outcome, err)
			}
			got := agyCheckEvidenceObservation(row, s, e)
			result := agyCheckClassify(got, 4)
			if got.Outcome != tc.evidence || result.OK != tc.ok || result.Observed != tc.evidence {
				t.Fatalf("observation=%+v classification=%+v", got, result)
			}
		})
	}
}

func TestAgyCheck149Alternatives(t *testing.T) {
	if !reflect.DeepEqual(agyCheckTranscriptToolAlternatives, map[string]string{"outside-read": "read_file"}) {
		t.Fatal("alternative tool names changed; review each additional row/tool")
	}
	for _, row := range agyCheckExpected {
		if got := agyCheckToolAccepted(row.ID, "read_file"); got != (row.ID == "outside-read") {
			t.Fatalf("read_file acceptance for %s: %t", row.ID, got)
		}
	}
}

func TestAgyCheck149TaskOn(t *testing.T) {
	for _, state := range []string{"DONE", "ERROR"} {
		t.Run(state, func(t *testing.T) {
			row, s, e := agyCheckEvidenceFixture(t, "task-on")
			if row.Expected != "denied" || !strings.Contains(row.Description, "1.4.9 observed denied once and allowed once") || strings.Contains(row.Description, "refuses") {
				t.Fatalf("unstable observation must retain stricter expectation: %+v", row)
			}
			e.Input.Transcript = []byte(agyCheckTestInit + agyCheckTestStep(0, "manage_task", state) + agyCheckTestResult)
			got := agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4)
			want := "allowed"
			if state == "ERROR" {
				want = "denied"
			}
			if got.OK != (state == "ERROR") || got.Observed != want {
				t.Fatalf("unstable outcome classification: %+v", got)
			}
		})
	}
}
