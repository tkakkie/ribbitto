package main

import (
	"fmt"
	"strings"
	"testing"
)

// agyCheckTranscriptFailures plants content that must never reach diagnostics.
func agyCheckTranscriptFailures() []struct{ name, data string } {
	const marker = "UNTRUSTED_TRANSCRIPT_MARKER"
	const end = "{\"event\":\"result\"}\n"
	step := "{\"event\":\"tool_step\",\"tool\":\"" + marker + "\",\"state\":\"ERROR\"}\n"
	return []struct{ name, data string }{
		{"empty", ""}, {"oversized", strings.Repeat(" ", agyCheckTranscriptMaxBytes) + end},
		{"too many lines", strings.Repeat("{\"event\":\"message\"}\n", agyCheckTranscriptMaxLines) + end},
		{"missing result", step}, {"result not last", end + step}, {"two results", end + end},
		{"truncated", step + strings.TrimSuffix(end, "\n")},
		{"duplicate keys", "{\"event\":\"message\",\"EVENT\":\"" + marker + "\"}\n" + end},
		{"duplicate tool", "{\"event\":\"tool_step\",\"tool\":\"" + marker + "\",\"TOOL\":\"other\",\"state\":\"ERROR\"}\n" + end},
		{"unknown event", "{\"event\":\"" + marker + "\"}\n" + end}, {"non JSON", marker + "\n" + end},
		{"not object", "[\"" + marker + "\"]\n" + end},
		{"trailing JSON", "{\"event\":\"message\"} {}\n" + end},
		{"invalid UTF8", "{\"event\":\"message\",\"text\":\"\xff\"}\n" + end},
		{"unpaired surrogate", "{\"event\":\"message\",\"text\":\"\\ud800\"}\n" + end},
		{"empty line", "\n" + end}, {"event missing", "{\"text\":\"" + marker + "\"}\n" + end},
		{"event number", "{\"event\":1}\n" + end}, {"event null", "{\"event\":null}\n" + end},
		{"event object", "{\"event\":{}}\n" + end},
		{"event empty", "{\"event\":\"\"}\n" + end}, {"event case", "{\"Event\":\"message\"}\n" + end},
		{"tool missing", "{\"event\":\"tool_step\",\"state\":\"ERROR\"}\n" + end},
		{"tool number", "{\"event\":\"tool_step\",\"tool\":1,\"state\":\"ERROR\"}\n" + end},
		{"tool null", "{\"event\":\"tool_step\",\"tool\":null,\"state\":\"ERROR\"}\n" + end},
		{"tool object", "{\"event\":\"tool_step\",\"tool\":{},\"state\":\"ERROR\"}\n" + end},
		{"tool empty", "{\"event\":\"tool_step\",\"tool\":\"\",\"state\":\"ERROR\"}\n" + end},
		{"unknown state", strings.Replace(step, "ERROR", marker, 1) + end},
		{"done case", strings.Replace(step, "ERROR", "done", 1) + end},
		{"error case", strings.Replace(step, "ERROR", "error", 1) + end},
		{"state missing", "{\"event\":\"tool_step\",\"tool\":\"" + marker + "\"}\n" + end},
		{"state null", strings.Replace(step, `"ERROR"`, "null", 1) + end},
		{"state number", strings.Replace(step, `"ERROR"`, "1", 1) + end},
		{"state object", strings.Replace(step, `"ERROR"`, "{}", 1) + end},
		{"batch number", strings.Replace(step, "}", `,"batch":1}`, 1) + end},
		{"batch null", strings.Replace(step, "}", `,"batch":null}`, 1) + end},
		{"batch object", strings.Replace(step, "}", `,"batch":{}}`, 1) + end},
		{"path missing", "{\"event\":\"hook_loaded\"}\n" + end},
		{"path number", "{\"event\":\"hook_loaded\",\"path\":1}\n" + end},
		{"path null", "{\"event\":\"hook_loaded\",\"path\":null}\n" + end},
		{"path object", "{\"event\":\"hook_loaded\",\"path\":{}}\n" + end},
		{"absolute path", "{\"event\":\"hook_loaded\",\"path\":\"/" + marker + "\"}\n" + end},
		{"parent path", "{\"event\":\"hook_loaded\",\"path\":\"../" + marker + "\"}\n" + end},
		{"backslash path", "{\"event\":\"hook_loaded\",\"path\":\"a\\\\" + marker + "\"}\n" + end},
		{"empty path", "{\"event\":\"hook_loaded\",\"path\":\"\"}\n" + end},
		{"dot path", "{\"event\":\"hook_loaded\",\"path\":\"./" + marker + "\"}\n" + end},
		{"empty component", "{\"event\":\"hook_loaded\",\"path\":\"a//" + marker + "\"}\n" + end},
		{"nul path", "{\"event\":\"hook_loaded\",\"path\":\"a\\u0000" + marker + "\"}\n" + end},
		{"noncontiguous batch", strings.Replace(step, "}", `,"batch":"a"}`, 1) + step + strings.Replace(step, "}", `,"batch":"a"}`, 1) + end},
	}
}

// TestAgyCheckTranscriptReject pins failures independently of the dispatcher.
func TestAgyCheckTranscriptReject(t *testing.T) {
	for _, tc := range agyCheckTranscriptFailures() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckTranscriptParse([]byte(tc.data))
			if err == nil || len(got.Steps) != 0 || len(got.LoadedPaths) != 0 {
				t.Fatalf("invalid transcript accepted or partial facts retained: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), "UNTRUSTED_TRANSCRIPT_MARKER") {
				t.Fatalf("diagnostic echoed transcript: %v", err)
			}
		})
	}
}

// TestAgyCheckTranscriptParse pins order and permits denied results, since
// applying review.go's success judgment here would reject the intended denies.
func TestAgyCheckTranscriptParse(t *testing.T) {
	data := []byte(`{"event":"message","text":"opaque"}
{"event":"thinking"}
{"event":"hook_loaded","path":".agent/hooks.json"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR","batch":"a"}
{"event":"tool_step","tool":"view_file","state":"DONE","batch":"a"}
{"event":"hook_loaded","path":"hooks.json"}
{"event":"tool_step","tool":"view_file","state":"DONE","batch":""}
{"event":"result","result":{"status":"FAILURE","denied_actions":["expected"]}}
`)
	got, err := agyCheckTranscriptParse(data)
	if err != nil || fmt.Sprint(got.Steps) != "[{write_to_file ERROR a} {view_file DONE a} {view_file DONE }]" || strings.Join(got.LoadedPaths, ",") != ".agent/hooks.json,hooks.json" {
		t.Fatalf("ordered facts: %+v %v", got, err)
	}
}

// TestAgyCheckTranscriptBatches distinguishes concurrency evidence from labels.
func TestAgyCheckTranscriptBatches(t *testing.T) {
	for _, tc := range []struct {
		ids     string
		want    int
		invalid bool
	}{
		{"", 0, false}, {"a", 0, false}, {"a a", 1, false}, {"a a b b", 2, false},
		{"a a - b b", 2, false}, {"a - a", 0, true}, {"a b a", 0, true},
	} {
		t.Run(tc.ids, func(t *testing.T) {
			var steps []agyCheckTranscriptStep
			for _, id := range strings.Fields(tc.ids) {
				if id == "-" {
					id = ""
				}
				steps = append(steps, agyCheckTranscriptStep{Batch: id})
			}
			got, err := agyCheckTranscriptParallelBatches(steps)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("batches=%d error=%v; want %d invalid=%t", got, err, tc.want, tc.invalid)
			}
		})
	}
}

// TestAgyCheckTranscriptOutcomes cross-checks the mapping domain against the
// core table, including both non-exercisable URL rows and silent exit 0.
func TestAgyCheckTranscriptOutcomes(t *testing.T) {
	if len(agyCheckTranscriptTools) != 20 {
		t.Fatal("tool mapping must cover exactly 20 rows")
	}
	for _, pair := range strings.Fields(`hook-decision=write_to_file hook-exit2=write_to_file hook-exit1=write_to_file hook-missing=write_to_file hook-timeout=write_to_file hook-unparseable=write_to_file hook-silent=write_to_file hook-precedence=write_to_file command-on=run_command command-off=run_command mcp-on=call_mcp_tool mcp-off=call_mcp_tool read-url=read_url execute-url=execute_url task-on=manage_task task-off=manage_task list-resources=list_resources read-resource=read_resource outside-write=write_to_file outside-read=view_file`) {
		id, tool, _ := strings.Cut(pair, "=")
		if agyCheckTranscriptTools[id] != tool {
			t.Fatalf("tool name for %s: %q want %q", id, agyCheckTranscriptTools[id], tool)
		}
	}
	count := 0
	for _, row := range agyCheckExpected {
		tool, exists := agyCheckTranscriptTools[row.ID]
		if exists != (row.Group != "discovery" && row.Group != "reconciliation") {
			t.Fatalf("tool coverage: %+v", row)
		}
		if !exists {
			continue
		}
		count++
		t.Run(row.ID, func(t *testing.T) {
			absent := "no step"
			if row.ID == "read-url" || row.ID == "execute-url" {
				absent = "not exercisable"
			}
			done := "allowed"
			if row.Expected == "answered" {
				done = "answered"
			}
			// Silence at exit 0 is a known ALLOW; hook-silent must use the same DONE rule.
			for _, tc := range []struct {
				steps []agyCheckTranscriptStep
				want  string
			}{
				{nil, absent}, {[]agyCheckTranscriptStep{{"other", "ERROR", ""}}, absent},
				{[]agyCheckTranscriptStep{{tool, "ERROR", ""}}, "denied"},
				{[]agyCheckTranscriptStep{{tool, "DONE", ""}}, done},
				{[]agyCheckTranscriptStep{{tool, "ERROR", ""}, {tool, "DONE", ""}}, "multiple steps"},
				{[]agyCheckTranscriptStep{{tool, "unknown", ""}}, "unparsed transcript"},
			} {
				if got := agyCheckTranscriptOutcome(row, tc.steps); got != tc.want {
					t.Fatalf("outcome=%q want %q", got, tc.want)
				}
			}
		})
	}
	if count != 20 || agyCheckTranscriptOutcome(agyCheckRow{ID: "unknown"}, nil) != "unknown row" {
		t.Fatal("mapping domain drifted")
	}
}
