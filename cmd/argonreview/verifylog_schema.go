package main

import "fmt"

const verifyLogToolName = "verify-log"

func verifyLogInputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"run":    map[string]any{"type": "string", "pattern": fmt.Sprintf("^[0-9a-f]{%d}$", verifyLogIDBytes*2)},
		"stream": map[string]any{"type": "string", "enum": []string{"index", "test", "package", "stderr", "output"}},
		"name":   map[string]any{"type": "string", "maxLength": verifyLogNameBytes, "description": fmt.Sprintf("Required only for test or package: #N (%d to %d ASCII digits, first nonzero) or an exact unique display name; at most %d UTF-8 bytes, without controls, ANSI, bidi or zero-width characters. Names beginning with # are reserved for ordinals. Never a path.", 1, verifyLogOrdinalDigits, verifyLogNameBytes)},
		"start":  map[string]any{"type": "string", "pattern": fmt.Sprintf("^[1-9][0-9]{0,%d}$", verifyLogStartDigits-1)},
	}, "required": []string{"run", "stream", "start"}, "additionalProperties": false}
}

func verifyLogTool() map[string]any {
	return map[string]any{"name": verifyLogToolName, "description": "Read retained failure output from this verification session by run ID and selector. Starts no check and takes no path.", "inputSchema": verifyLogInputSchema()}
}
