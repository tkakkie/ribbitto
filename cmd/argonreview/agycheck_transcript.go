package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// agyCheckTranscriptParse assumptions are unverified against real agy. From
// the issue and parseTranscript only: stream-json is newline-terminated JSON
// objects with distinct (case-insensitive) keys and a nonempty string event;
// only the last event is result. The deliberately closed event table below
// must be extended by a reviewed change after the first host run, never guessed.
// tool_step requires string tool and exact DONE/ERROR state; optional string
// batch groups contiguous steps, with at least two steps proving parallelism.
// hook_loaded requires a clean relative workspace path. Malformed Unicode is
// refused before decoding, because encoding/json otherwise repairs it. Other
// event fields are opaque JSON: result status/response/denied_actions are not success evidence,
// since denies are expected here. Row tool names in the outcome table are also
// assumptions. Bounds and fixed diagnostics prevent untrusted output from
// becoming report text. No partial result survives a malformed event or batch.
func agyCheckTranscriptParse(data []byte) (agyCheckTranscriptResult, error) {
	if len(data) == 0 || len(data) > agyCheckTranscriptMaxBytes || !bytes.HasSuffix(data, []byte{'\n'}) {
		return agyCheckTranscriptResult{}, errors.New("invalid transcript framing")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) > agyCheckTranscriptMaxLines {
		return agyCheckTranscriptResult{}, errors.New("transcript line limit exceeded")
	}
	var result agyCheckTranscriptResult
	for i, line := range lines {
		invalid := func() (agyCheckTranscriptResult, error) {
			return agyCheckTranscriptResult{}, fmt.Errorf("invalid transcript event at line %d", i+1)
		}
		if distinctKeys(line) != nil || !implementGuardUnicode(line) {
			return invalid()
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &fields) != nil {
			return invalid()
		}
		event, ok := agyCheckTranscriptString(fields, "event")
		if !ok || event == "" || !agyCheckTranscriptEvents[event] {
			return invalid()
		}
		if (event == "result") != (i == len(lines)-1) {
			return invalid()
		}
		switch event {
		case "tool_step":
			tool, toolOK := agyCheckTranscriptString(fields, "tool")
			state, stateOK := agyCheckTranscriptString(fields, "state")
			if !toolOK || tool == "" || !stateOK || (state != "DONE" && state != "ERROR") {
				return invalid()
			}
			batch := ""
			if _, exists := fields["batch"]; exists {
				var batchOK bool
				batch, batchOK = agyCheckTranscriptString(fields, "batch")
				if !batchOK {
					return invalid()
				}
			}
			result.Steps = append(result.Steps, agyCheckTranscriptStep{tool, state, batch})
		case "hook_loaded":
			path, pathOK := agyCheckTranscriptString(fields, "path")
			if !pathOK || !agyCheckTranscriptPath(path) {
				return invalid()
			}
			result.LoadedPaths = append(result.LoadedPaths, path)
		}
	}
	if _, err := agyCheckTranscriptParallelBatches(result.Steps); err != nil {
		return agyCheckTranscriptResult{}, err
	}
	return result, nil
}

// agyCheckTranscriptUnparsed prevents parser details becoming report text.
const agyCheckTranscriptUnparsed = "unparsed transcript"

// agyCheckTranscriptMaxBytes bounds allocation before decoding untrusted data.
const agyCheckTranscriptMaxBytes = 4 << 20

// agyCheckTranscriptMaxLines bounds even very small events.
const agyCheckTranscriptMaxLines = 16384

// agyCheckTranscriptEvents intentionally refuses new event names until reviewed.
var agyCheckTranscriptEvents = map[string]bool{
	"tool_step": true, "hook_loaded": true, "message": true, "thinking": true, "result": true,
}

// agyCheckTranscriptStep preserves order because totals cannot prove batches.
type agyCheckTranscriptStep struct{ Tool, State, Batch string }

// agyCheckTranscriptResult keeps loaded evidence separate from hook execution.
type agyCheckTranscriptResult struct {
	Steps       []agyCheckTranscriptStep
	LoadedPaths []string
}

// agyCheckTranscriptString rejects null rather than letting JSON repair it to "".
func agyCheckTranscriptString(fields map[string]json.RawMessage, name string) (string, bool) {
	var value string
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// agyCheckTranscriptPath refuses aliases without consulting a filesystem.
func agyCheckTranscriptPath(path string) bool {
	if strings.ContainsAny(path, "\x00\\") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// agyCheckTranscriptParallelBatches rejects reused IDs: disjoint steps cannot
// establish one parallel batch, even when their totals happen to reconcile.
func agyCheckTranscriptParallelBatches(steps []agyCheckTranscriptStep) (int, error) {
	counts := make(map[string]int)
	previous := ""
	for _, step := range steps {
		if step.Batch != "" {
			if counts[step.Batch] > 0 && previous != step.Batch {
				return 0, errors.New("non-contiguous transcript batch")
			}
			counts[step.Batch]++
		}
		previous = step.Batch
	}
	parallel := 0
	for _, count := range counts {
		if count >= 2 {
			parallel++
		}
	}
	return parallel, nil
}

// agyCheckTranscriptTools pins single-call assumptions independently of the
// expected outcomes, so changing an expectation cannot invent a tool step.
var agyCheckTranscriptTools = map[string]string{
	"hook-decision": "write_to_file", "hook-exit2": "write_to_file", "hook-exit1": "write_to_file",
	"hook-missing": "write_to_file", "hook-timeout": "write_to_file", "hook-unparseable": "write_to_file",
	"hook-silent": "write_to_file", "hook-precedence": "write_to_file",
	"command-on": "run_command", "command-off": "run_command",
	"mcp-on": "call_mcp_tool", "mcp-off": "call_mcp_tool",
	"read-url": "read_url", "execute-url": "execute_url",
	"task-on": "manage_task", "task-off": "manage_task",
	"list-resources": "list_resources", "read-resource": "read_resource",
	"outside-write": "write_to_file", "outside-read": "view_file",
}

// agyCheckTranscriptOutcome never copies tool text into the core's report.
func agyCheckTranscriptOutcome(row agyCheckRow, steps []agyCheckTranscriptStep) string {
	tool, known := agyCheckTranscriptTools[row.ID]
	if !known {
		return "unknown row"
	}
	count, state := 0, ""
	for _, step := range steps {
		if step.Tool == tool {
			count++
			state = step.State
		}
	}
	if count > 1 {
		return "multiple steps"
	}
	if count == 0 {
		if row.ID == "read-url" || row.ID == "execute-url" {
			return "not exercisable"
		}
		return "no step"
	}
	if state == "ERROR" {
		return "denied"
	}
	if state != "DONE" {
		return agyCheckTranscriptUnparsed
	}
	if row.Expected == "answered" {
		return "answered"
	}
	return "allowed"
}
