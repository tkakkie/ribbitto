package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

// agyCheckTranscriptParse follows 1.4.7 samples and two hand captures of
// 1.4.9's stream format (2026-10-30, #1978), accepting streamed agent_response
// updates. Hook-load events, exact deny error text and other tool names remain
// unverified; 1.4.9 still needs a host run of the check. Opaque
// fields, including result status and tool errors, never become report text.
// Unicode is checked before decoding; malformed events discard every fact.
func agyCheckTranscriptParse(data []byte) (agyCheckTranscriptResult, error) {
	if len(data) == 0 || len(data) > agyCheckTranscriptMaxBytes || !bytes.HasSuffix(data, []byte{'\n'}) {
		return agyCheckTranscriptResult{}, errors.New("invalid transcript framing")
	}
	// Count before splitting so tiny-line floods cannot allocate a slice per line.
	if bytes.Count(data, []byte{'\n'}) > agyCheckTranscriptMaxLines {
		return agyCheckTranscriptResult{}, errors.New("transcript line limit exceeded")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	var result agyCheckTranscriptResult
	type progress struct{ kind, tool, state string }
	seen := make(map[int]progress)
	for i, line := range lines {
		invalid := func() (agyCheckTranscriptResult, error) {
			return agyCheckTranscriptResult{}, fmt.Errorf("invalid transcript event at line %d", i+1)
		}
		if !implementGuardUnicode(line) || !agyCheckTranscriptValidate(line) {
			return invalid()
		}
		fields, ok := agyCheckTranscriptObject(line)
		if !ok {
			return invalid()
		}
		event, ok := agyCheckTranscriptString(fields, "event")
		if !ok || !agyCheckTranscriptEvents[event] || (event == "init") != (i == 0) || (event == "result") != (i == len(lines)-1) {
			return invalid()
		}
		switch event {
		case "result":
			if _, ok := agyCheckTranscriptObject(fields["result"]); !ok {
				return invalid()
			}
		case "step_update":
			step, ok := agyCheckTranscriptObject(fields["step_update"])
			if !ok {
				return invalid()
			}
			var index int
			if raw := step["step_index"]; len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &index) != nil || index < 0 {
				return invalid()
			}
			state, stateOK := agyCheckTranscriptString(step, "state")
			kind, kindOK := agyCheckTranscriptString(step, "step_type")
			if !stateOK || (state != "ACTIVE" && state != "DONE" && state != "ERROR") || !kindOK || (kind != "user_input" && kind != "agent_response" && kind != "tool") {
				return invalid()
			}
			tool := ""
			if kind == "tool" {
				var toolOK bool
				tool, toolOK = agyCheckTranscriptString(step, "tool_name")
				if !toolOK || tool == "" {
					return invalid()
				}
			}
			if raw, exists := step["tool_info"]; exists {
				info, ok := agyCheckTranscriptObject(raw)
				if !ok {
					return invalid()
				}
				if _, exists := info["name"]; exists {
					name, ok := agyCheckTranscriptString(info, "name")
					if !ok || name != tool {
						return invalid()
					}
				}
				if raw, exists := info["error"]; exists {
					failure, ok := agyCheckTranscriptObject(raw)
					if !ok {
						return invalid()
					}
					if _, ok := agyCheckTranscriptString(failure, "message"); !ok {
						return invalid()
					}
				}
			}
			if prior, exists := seen[index]; exists && (prior.state != "ACTIVE" || (state == "ACTIVE" && kind != "agent_response") || prior.kind != kind || prior.tool != tool) {
				return invalid()
			}
			seen[index] = progress{kind, tool, state}
			if kind == "tool" && state != "ACTIVE" {
				result.Steps = append(result.Steps, agyCheckTranscriptStep{tool, state})
			}
		}
	}
	// Completion events can arrive out of order; response boundaries use step_index.
	indices := make([]int, 0, len(seen))
	for index := range seen {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	group := 0
	for _, index := range indices {
		step := seen[index]
		if step.kind == "agent_response" {
			result.Groups = append(result.Groups, group)
			group = 0
		}
		if step.kind == "tool" && step.state != "ACTIVE" {
			group++
		}
	}
	result.Groups = append(result.Groups, group)
	for _, step := range seen {
		if step.kind == "tool" && step.state == "ACTIVE" {
			result.Unfinished++
		}
	}
	return result, nil
}

// Bound all containers, including opaque parameters, before copying consumed fields.
const agyCheckTranscriptMaxDepth = 16
const agyCheckTranscriptMaxKeys = 256

// agyCheckTranscriptValidate walks each line once; object key sets are scoped
// to their level so opaque payloads cannot force repeated subtree scans.
func agyCheckTranscriptValidate(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') || !agyCheckTranscriptValue(decoder, first, 1) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func agyCheckTranscriptValue(decoder *json.Decoder, token json.Token, depth int) bool {
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	if depth > agyCheckTranscriptMaxDepth {
		return false
	}
	var keys map[string]bool
	if delim == '{' {
		keys = make(map[string]bool)
	}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || len(keys) >= agyCheckTranscriptMaxKeys {
				return false
			}
			// Canonicalise the full simple-fold cycle to preserve EqualFold's
			// Unicode policy (including long s and final sigma) with a set.
			folded := strings.Map(func(r rune) rune {
				least := r
				for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
					least = min(least, next)
				}
				return least
			}, name)
			if keys[folded] {
				return false
			}
			keys[folded] = true
		}
		value, err := decoder.Token()
		if err != nil || !agyCheckTranscriptValue(decoder, value, depth+1) {
			return false
		}
	}
	end, err := decoder.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

// agyCheckTranscriptObject decodes only consumed objects after line validation.
func agyCheckTranscriptObject(raw []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// agyCheckTranscriptUnparsed prevents parser details becoming report text.
const agyCheckTranscriptUnparsed = "unparsed transcript"

const agyCheckLoadUnobservable = "hook load not observable in the transcript"
const agyCheckBatchesUnobservable = "parallel batches not observable in the transcript"

// agyCheckTranscriptMaxBytes bounds allocation before decoding untrusted data.
const agyCheckTranscriptMaxBytes = 4 << 20

// agyCheckTranscriptMaxLines bounds even very small events.
const agyCheckTranscriptMaxLines = 16384

// agyCheckTranscriptEvents intentionally refuses new event names until reviewed.
var agyCheckTranscriptEvents = map[string]bool{
	"init": true, "step_update": true, "result": true,
}

// agyCheckTranscriptStep preserves final-event order.
type agyCheckTranscriptStep struct{ Tool, State string }

type agyCheckTranscriptResult struct {
	Steps      []agyCheckTranscriptStep
	Groups     []int
	Unfinished int
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
	for i := 0; i < len(path); i++ {
		if path[i] < 0x20 || path[i] == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// agyCheckTranscriptTools pins single-call assumptions independently of the
// expected outcomes, so changing an expectation cannot invent a tool step.
// Only write_file and call_mcp_tool are verified; other names are unverified.
var agyCheckTranscriptTools = map[string]string{
	"discovery-agent": "write_file", "discovery-agents": "write_file",
	"discovery-gemini-config": "write_file", "discovery-gemini": "write_file",
	"discovery-antigravity": "write_file", "discovery-root": "write_file",
	"reconciliation": "write_file",
	"hook-decision":  "write_file", "hook-exit2": "write_file", "hook-exit1": "write_file",
	"hook-missing": "write_file", "hook-timeout": "write_file", "hook-unparseable": "write_file",
	"hook-silent": "write_file", "hook-precedence": "write_file",
	"hook-same-name-agent": "write_file", "hook-same-name-agents": "write_file",
	"command-on": "run_command", "command-off": "run_command",
	"mcp-on": "call_mcp_tool", "mcp-off": "call_mcp_tool",
	"read-url": "read_url", "execute-url": "execute_url",
	"task-on": "manage_task", "task-off": "manage_task",
	"list-resources": "list_resources", "read-resource": "read_resource",
	"outside-write": "write_file", "outside-read": "view_file",
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
