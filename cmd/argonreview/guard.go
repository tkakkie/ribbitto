package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// Reviews only read the worktree. Deny rules cannot name search_web,
// subagents, generate_image or schedule, so this hook refuses every tool
// not listed (#1041 preflight 3). Reads stay inside the worktree because the
// configuration grants no read_file and headless mode refuses reads outside.
var allowedTools = map[string]bool{"view_file": true, "list_dir": true, "grep_search": true, "find_by_name": true}

const maxGuardInput = 1 << 20

func decide(input []byte) (bool, string) {
	if len(input) > maxGuardInput {
		return false, "hook input too large"
	}
	var call struct {
		ToolCall *struct {
			Name string `json:"name"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(input, &call); err != nil {
		return false, "unreadable hook input"
	}
	if call.ToolCall == nil || call.ToolCall.Name == "" {
		return false, "hook input names no tool"
	}
	if !allowedTools[call.ToolCall.Name] {
		return false, fmt.Sprintf("%s is not allowed in a review", call.ToolCall.Name)
	}
	return true, ""
}

// guard prints one decision and returns the process status: 0 only once an
// allow decision has been written, and non-zero for every deny, unreadable
// input or failed write. Antigravity treats a hook that prints nothing and
// exits 0 as an allow, and any non-zero exit as a refusal whatever the hook
// printed (#1071, #1130), so the status is what keeps a lost answer a deny.
// The reason also goes to stderr, which Antigravity puts in the tool error.
func guard(stdin io.Reader, stdout, stderr io.Writer) int {
	allow, reason := false, "unreadable hook input"
	if input, err := io.ReadAll(io.LimitReader(stdin, maxGuardInput+1)); err == nil {
		allow, reason = decide(input)
	}
	decision := map[string]string{"decision": "deny", "reason": reason}
	if allow {
		decision = map[string]string{"decision": "allow"}
	}
	if err := json.NewEncoder(stdout).Encode(decision); err != nil {
		return 1
	}
	if allow {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "argonreview guard: %s\n", reason)
	return 2
}
