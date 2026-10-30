package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const agyCheckTestInit = "{\"event\":\"init\",\"conversation_id\":\"d11e3b12-740e-440d-85a5-d50e2d939177\",\"init\":{\"model\":\"gemini-4-argon-high\",\"permission_mode\":\"request-review\",\"cwd\":\"...\",\"tools\":[\"...\"]}}\n"
const agyCheckTestResult = "{\"event\":\"result\",\"result\":{}}\n"

func agyCheckTestStep(index int, tool, state string) string {
	return fmt.Sprintf("{\"event\":\"step_update\",\"step_update\":{\"step_index\":%d,\"state\":%q,\"step_type\":\"tool\",\"tool_name\":%q}}\n", index, state, tool)
}

func agyCheckTestResponse(index int, kind, state string) string {
	return fmt.Sprintf("{\"event\":\"step_update\",\"step_update\":{\"step_index\":%d,\"state\":%q,\"step_type\":%q,\"text_delta\":\"x\",\"duration_seconds\":1,\"usage\":{\"output_tokens\":1}}}\n", index, state, kind)
}

// Synthetic #1978 capture shape; no captured model text or deny wording.
func agyCheckTestStream(count int, complete bool) string {
	data := agyCheckTestInit + agyCheckTestResponse(0, "user_input", "DONE") + agyCheckTestResponse(1, "agent_response", "DONE")
	for _, state := range []string{"ACTIVE", "ERROR"} {
		info := `,"tool_info":{"name":"write_file","parameters":{"Path":"..."}`
		if state == "ERROR" {
			info += `,"error":{"type":"TOOL_ERROR","message":"x"}`
		}
		data += strings.TrimSuffix(agyCheckTestStep(2, "write_file", state), "}}\n") + info + "}}}\n"
	}
	data += strings.Repeat(agyCheckTestResponse(3, "agent_response", "ACTIVE"), count)
	if complete {
		data += agyCheckTestResponse(3, "agent_response", "DONE")
	}
	return data + "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\"}}\n"
}

func TestAgyCheckTranscriptStreaming(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       agyCheckTranscriptResult
	}{
		{"13 deltas", agyCheckTestStream(13, true), agyCheckTranscriptResult{Steps: []agyCheckTranscriptStep{{"write_file", "ERROR"}}, Groups: []int{0, 1, 0}}},
		{"31 deltas", agyCheckTestStream(31, true), agyCheckTranscriptResult{Steps: []agyCheckTranscriptStep{{"write_file", "ERROR"}}, Groups: []int{0, 1, 0}}},
		{"response left ACTIVE", agyCheckTestStream(13, false), agyCheckTranscriptResult{Steps: []agyCheckTranscriptStep{{"write_file", "ERROR"}}, Groups: []int{0, 1, 0}}},
		{"between tools", strings.Replace(agyCheckTestStream(31, true), `{"event":"result"`, agyCheckTestStep(4, "write_file", "DONE")+agyCheckTestStep(5, "write_file", "ACTIVE")+`{"event":"result"`, 1), agyCheckTranscriptResult{Steps: []agyCheckTranscriptStep{{"write_file", "ERROR"}, {"write_file", "DONE"}}, Groups: []int{0, 1, 1}, Unfinished: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckTranscriptParse([]byte(tc.data))
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("facts=%+v want %+v: %v", got, tc.want, err)
			}
		})
	}
}

// agyCheckTranscriptFailures plants content that must never reach diagnostics.
func agyCheckTranscriptFailures() []struct{ name, data string } {
	const marker = "UNTRUSTED_TRANSCRIPT_MARKER"
	step := agyCheckTestStep(2, marker, "ERROR")
	wrap := func(line string) string {
		return agyCheckTestInit + agyCheckTestStep(100, "write_file", "DONE") + agyCheckTestStep(101, "write_file", "ACTIVE") + line + agyCheckTestResult
	}
	cases := []struct{ name, data string }{
		{"empty", ""}, {"oversized", strings.Repeat(" ", agyCheckTranscriptMaxBytes) + agyCheckTestResult},
		{"too many lines", strings.Repeat("\n", agyCheckTranscriptMaxLines+1)},
		{"missing init", step + agyCheckTestResult}, {"duplicate init", wrap(agyCheckTestInit)},
		{"missing result", agyCheckTestInit + step}, {"result not last", wrap(agyCheckTestResult + step)},
		{"two results", wrap(agyCheckTestResult)}, {"truncated", strings.TrimSuffix(wrap(step), "\n")},
		{"duplicate keys", wrap(`{"event":"step_update","EVENT":"` + marker + `"}` + "\n")},
		{"unknown event", wrap(`{"event":"` + marker + `"}` + "\n")},
		{"tool_step", wrap(`{"event":"tool_step","tool":"write_file","state":"DONE"}` + "\n")},
		{"hook_loaded", wrap(`{"event":"hook_loaded","path":"hooks.json"}` + "\n")},
		{"non JSON", wrap(marker + "\n")}, {"not object", wrap(`["` + marker + `"]` + "\n")},
		{"trailing JSON", wrap(strings.TrimSuffix(step, "\n") + " {}\n")},
		{"invalid UTF8", wrap("{\"event\":\"step_update\",\"text\":\"\xff\"}\n")},
		{"unpaired surrogate", wrap(`{"event":"step_update","text":"\ud800"}` + "\n")},
		{"empty line", wrap("\n")},
		{"two finals", wrap(step + step)},
		{"ERROR then DONE", wrap(step + agyCheckTestStep(2, marker, "DONE"))},
		{"DONE then ERROR", wrap(agyCheckTestStep(2, marker, "DONE") + step)},
		{"ACTIVE after final", wrap(step + agyCheckTestStep(2, marker, "ACTIVE"))},
		{"repeated ACTIVE", wrap(agyCheckTestStep(2, marker, "ACTIVE") + agyCheckTestStep(2, marker, "ACTIVE"))},
		{"changed tool", wrap(agyCheckTestStep(2, "other", "ACTIVE") + step)},
		{"changed type", wrap(strings.Replace(agyCheckTestStep(2, marker, "ACTIVE"), `"tool"`, `"user_input"`, 1) + step)},
	}
	active := agyCheckTestResponse(3, "agent_response", "ACTIVE")
	stream := strings.Repeat(active, 13)
	done := agyCheckTestResponse(3, "agent_response", "DONE")
	failure := agyCheckTestResponse(3, "agent_response", "ERROR")
	for _, tc := range []struct{ name, data string }{
		{"agent ACTIVE after DONE", stream + done + active},
		{"agent ACTIVE after ERROR", stream + failure + active},
		{"agent DONE then DONE", stream + done + done},
		{"agent DONE then ERROR", stream + done + failure},
		{"stream agent to tool", stream + agyCheckTestStep(3, marker, "DONE")},
		{"stream tool to agent", agyCheckTestStep(3, marker, "ACTIVE") + stream + done},
		{"stream agent to user", stream + agyCheckTestResponse(3, "user_input", "DONE")},
		{"stream user to agent", agyCheckTestResponse(3, "user_input", "ACTIVE") + stream + done},
		{"user repeated ACTIVE", strings.Repeat(agyCheckTestResponse(3, "user_input", "ACTIVE"), 2)},
	} {
		cases = append(cases, struct{ name, data string }{tc.name, wrap(tc.data)})
	}
	for _, field := range []string{"event", "step_index", "state", "step_type", "tool_name"} {
		for _, value := range []string{"null", "{}", "1", `""`} {
			// Integer 1 is a valid index; other fields require strings.
			if field == "step_index" && value == "1" {
				continue
			}
			old := map[string]string{"event": `"step_update"`, "step_index": "2", "state": `"ERROR"`, "step_type": `"tool"`, "tool_name": `"` + marker + `"`}[field]
			cases = append(cases, struct{ name, data string }{field + "/" + value, wrap(strings.Replace(step, `"`+field+`":`+old, `"`+field+`":`+value, 1))})
		}
		old := map[string]string{"event": `"event":"step_update",`, "step_index": `"step_index":2,`, "state": `"state":"ERROR",`, "step_type": `"step_type":"tool",`, "tool_name": `,"tool_name":"` + marker + `"`}[field]
		cases = append(cases, struct{ name, data string }{field + " missing", wrap(strings.Replace(step, old, "", 1))})
	}
	for _, pair := range [][2]string{
		{"negative index", `"step_index":-1`}, {"fractional index", `"step_index":2.5`}, {"string index", `"step_index":"2"`},
		{"overflow index", `"step_index":9999999999999999999999999`},
		{"bad state", `"state":"done"`}, {"bad type", `"step_type":"other"`},
		{"nested duplicate", `"state":"ERROR","STATE":"DONE"`},
	} {
		field, _, _ := strings.Cut(pair[1], ":")
		old := map[string]string{`"step_index"`: `"step_index":2`, `"state"`: `"state":"ERROR"`, `"step_type"`: `"step_type":"tool"`}[field]
		cases = append(cases, struct{ name, data string }{pair[0], wrap(strings.Replace(step, old, pair[1], 1))})
	}
	for _, info := range []string{`null`, `1`, `"text"`, `{}`, `{"name":null}`, `{"name":1}`, `{"name":"other"}`, `{"name":"` + marker + `","NAME":"` + marker + `"}`, `{"error":null}`, `{"error":1}`, `{"error":{}}`, `{"error":{"message":null}}`, `{"error":{"message":1}}`, `{"error":{"message":"a","MESSAGE":"b"}}`, `{"parameters":{"Path":"a","PATH":"b"}}`, `{"parameters":[{"s":1,"\u017f":2}]}`, `{"parameters":[{"σ":1,"ς":2}]}`} {
		if info == `{}` {
			continue
		} // tool_info itself is optional and may be empty.
		line := strings.TrimSuffix(step, "}}\n") + `,"tool_info":` + info + "}}\n"
		cases = append(cases, struct{ name, data string }{"tool_info/" + info, wrap(line)})
	}
	cases = append(cases, struct{ name, data string }{"duplicate result", agyCheckTestInit + "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"STATUS\":\"FAILURE\"}}\n"})
	for _, value := range []string{"null", "1", `"text"`} {
		cases = append(cases, struct{ name, data string }{"step object/" + value, wrap("{\"event\":\"step_update\",\"step_update\":" + value + "}\n")}, struct{ name, data string }{"result object/" + value, agyCheckTestInit + "{\"event\":\"result\",\"result\":" + value + "}\n"})
	}
	return cases
}

func TestAgyCheckTranscriptReject(t *testing.T) {
	for _, tc := range agyCheckTranscriptFailures() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckTranscriptParse([]byte(tc.data))
			if err == nil || !reflect.DeepEqual(got, agyCheckTranscriptResult{}) {
				t.Fatalf("invalid transcript accepted or partial facts retained: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), "UNTRUSTED_TRANSCRIPT_MARKER") {
				t.Fatalf("diagnostic echoed transcript: %v", err)
			}
		})
	}
}

func TestAgyCheckTranscriptLineLimit(t *testing.T) {
	for _, n := range []int{agyCheckTranscriptMaxLines, agyCheckTranscriptMaxLines + 1} {
		data := agyCheckTestInit
		var body strings.Builder
		body.WriteString(data)
		for i := 0; i < n-2; i++ {
			fmt.Fprintf(&body, "{\"event\":\"step_update\",\"step_update\":{\"step_index\":%d,\"state\":\"DONE\",\"step_type\":\"agent_response\"}}\n", i)
		}
		data = body.String() + agyCheckTestResult
		if len(data) >= agyCheckTranscriptMaxBytes {
			t.Fatal("fixture exceeds byte bound")
		}
		got, err := agyCheckTranscriptParse([]byte(data))
		if n == agyCheckTranscriptMaxLines {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || err.Error() != "transcript line limit exceeded" {
			t.Fatal(err)
		}
		if len(got.Steps) != 0 || got.Unfinished != 0 {
			t.Fatal("invented facts")
		}
	}
}

// Parameters are agent-controlled; limits include containers in opaque fields.
func TestAgyCheckTranscriptDepthLimit(t *testing.T) {
	for _, container := range []string{"object", "array"} {
		for _, depth := range []int{16, 17} {
			t.Run(fmt.Sprintf("%s/depth-%d", container, depth), func(t *testing.T) {
				payload := `"opaque"`
				// The event, step_update and tool_info already occupy three levels.
				for i := 0; i < depth-3; i++ {
					if container == "object" {
						payload = `{"a":` + payload + `}`
					} else {
						payload = `[` + payload + `]`
					}
				}
				data := agyCheckTestParameters(payload)
				got, err := agyCheckTranscriptParse([]byte(data))
				if (err == nil) != (depth == 16) || (err != nil && len(got.Steps) != 0) {
					t.Fatalf("depth %d: %+v %v", depth, got, err)
				}
			})
		}
	}
}

func TestAgyCheckTranscriptKeyLimit(t *testing.T) {
	for _, location := range []string{"top", "parameters"} {
		for _, count := range []int{256, 257} {
			t.Run(fmt.Sprintf("%s/keys-%d", location, count), func(t *testing.T) {
				fields := []string{`"event":"init"`}
				if location == "parameters" {
					fields = nil
				}
				for i := len(fields); i < count; i++ {
					fields = append(fields, fmt.Sprintf(`"key%d":0`, i))
				}
				object := "{" + strings.Join(fields, ",") + "}"
				data := object + "\n" + agyCheckTestResult
				if location == "parameters" {
					data = agyCheckTestParameters(object)
				}
				got, err := agyCheckTranscriptParse([]byte(data))
				if (err == nil) != (count == 256) || (err != nil && len(got.Steps) != 0) {
					t.Fatalf("keys %d: %+v %v", count, got, err)
				}
			})
		}
	}
}

func agyCheckTestParameters(payload string) string {
	return agyCheckTestInit + `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"write_file","tool_info":{"parameters":` + payload + "}}}\n" + agyCheckTestResult
}

func TestAgyCheckTranscriptDeepPayload(t *testing.T) {
	payload := strings.Repeat(`{"a":`, 9000) + `"` + strings.Repeat("x", 256<<10) + `"` + strings.Repeat("}", 9000)
	data := []byte(agyCheckTestParameters(payload))
	if len(data) > agyCheckTranscriptMaxBytes {
		t.Fatal("fixture exceeds byte bound")
	}
	start := time.Now()
	got, err := agyCheckTranscriptParse(data)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("deep payload took %v to refuse", elapsed)
	}
	if err == nil || len(got.Steps) != 0 || got.Unfinished != 0 {
		t.Fatalf("deep payload accepted or partial facts retained: %+v %v", got, err)
	}
}

// Sanitised complete shapes from #1972; errors and result text are opaque.
const agyCheckRealLines = `{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":6.8,"usage":{"input_tokens":8213,"output_tokens":306,"thinking_tokens":267,"cache_read_tokens":0,"total_tokens":8519}}}
{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"write_file","tool_info":{"name":"write_file","parameters":{"Path":"..."}}}}
{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":2,"state":"DONE","step_type":"tool","tool_name":"write_file","duration_seconds":6.4,"tool_info":{"name":"write_file","parameters":{"Path":"..."}}}}
{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":3,"state":"ERROR","step_type":"tool","tool_name":"write_file","duration_seconds":6.1,"tool_info":{"name":"write_file","parameters":{"Path":"..."},"error":{"type":"TOOL_ERROR","message":"JSON hook \"<name>\" failed: command failed: exit status 2, stderr: <text>"}}}}
{"event":"step_update","step_update":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","step_index":4,"state":"ERROR","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"<server>","ToolName":"<tool>"},"error":{"type":"TOOL_ERROR","message":"permission check failed for mcp \"<server>/<tool>\": user denied permission for mcp(<server>/<tool>)"}}}}
{"event":"result","result":{"conversation_id":"d11e3b12-740e-440d-85a5-d50e2d939177","status":"SUCCESS","response":"","duration_seconds":19.2,"num_turns":1,"usage":{"total_tokens":11031},"denied_actions":[{"action":"mcp","display_name":"CallMcpTool"}]}}
`

func TestAgyCheckTranscriptParse(t *testing.T) {
	lines := strings.SplitAfter(agyCheckRealLines, "\n")
	for _, tc := range []struct {
		name, data, want string
		unfinished       int
	}{
		{"full", agyCheckRealLines, "[{write_file DONE} {write_file ERROR} {call_mcp_tool ERROR}]", 0},
		{"hook failure", lines[4] + agyCheckTestResult, "[{write_file ERROR}]", 0},
		{"MCP denial", lines[5] + agyCheckTestResult, "[{call_mcp_tool ERROR}]", 0},
		{"ACTIVE only", lines[2] + agyCheckTestResult, "[]", 1},
		{"final order", agyCheckTestStep(2, "write_file", "ACTIVE") + agyCheckTestStep(3, "call_mcp_tool", "ERROR") + agyCheckTestStep(2, "write_file", "DONE") + agyCheckTestResult, "[{call_mcp_tool ERROR} {write_file DONE}]", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckTranscriptParse([]byte(agyCheckTestInit + tc.data))
			if err != nil || fmt.Sprint(got.Steps) != tc.want || got.Unfinished != tc.unfinished {
				t.Fatalf("facts: %+v %v", got, err)
			}
		})
	}
}

// TestAgyCheckTranscriptOutcomes cross-checks the mapping domain against the
// core table, including both non-exercisable URL rows and silent exit 0.
func TestAgyCheckTranscriptOutcomes(t *testing.T) {
	if len(agyCheckTranscriptTools) != 29 {
		t.Fatal("tool mapping must cover exactly 29 scenarios (30 rows)")
	}
	for _, pair := range strings.Fields(`hook-decision=write_file hook-exit2=write_file hook-exit1=write_file hook-missing=write_file hook-timeout=write_file hook-unparseable=write_file hook-silent=write_file hook-precedence=write_file hook-same-name-agent=write_file hook-same-name-agents=write_file command-on=run_command command-off=run_command mcp-on=call_mcp_tool mcp-off=call_mcp_tool read-url=read_url execute-url=execute_url task-on=manage_task task-off=manage_task list-resources=list_resources read-resource=read_resource outside-write=write_file outside-read=view_file`) {
		id, tool, _ := strings.Cut(pair, "=")
		if agyCheckTranscriptTools[id] != tool {
			t.Fatalf("tool name for %s: %q want %q", id, agyCheckTranscriptTools[id], tool)
		}
	}
	count := 0
	for _, row := range agyCheckExpected {
		if row.ID == "parallel-batches" {
			continue
		} // Same transcript as reconciliation.
		tool, exists := agyCheckTranscriptTools[row.ID]
		if !exists || tool == "" {
			t.Fatalf("tool coverage: %+v", row)
		}
		if row.Group == "discovery" || row.Group == "reconciliation" {
			if tool != "write_file" {
				t.Fatalf("file scenario tool changed: %s", row.ID)
			}
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
				{nil, absent}, {[]agyCheckTranscriptStep{{"other", "ERROR"}}, absent},
				{[]agyCheckTranscriptStep{{tool, "ERROR"}}, "denied"},
				{[]agyCheckTranscriptStep{{tool, "DONE"}}, done},
				{[]agyCheckTranscriptStep{{tool, "ERROR"}, {tool, "DONE"}}, "multiple steps"},
				{[]agyCheckTranscriptStep{{tool, "unknown"}}, "unparsed transcript"},
			} {
				if got := agyCheckTranscriptOutcome(row, tc.steps); got != tc.want {
					t.Fatalf("outcome=%q want %q", got, tc.want)
				}
			}
		})
	}
	if count != 29 || agyCheckTranscriptOutcome(agyCheckRow{ID: "unknown"}, nil) != "unknown row" {
		t.Fatal("mapping domain drifted")
	}
}
