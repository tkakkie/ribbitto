package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const agyCheckStubUsage = "agy-check stub-mcp <dir>"
const agyCheckStubURI = "ribbitto-check://fixture"

type agyCheckStubDeps struct{ hook agyCheckHookDeps }

func agyCheckStub(args []string, stdout, stderr io.Writer, d agyCheckStubDeps) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: argonreview "+agyCheckStubUsage)
		return 2
	}
	fail := func() int {
		_, _ = fmt.Fprintln(stderr, "agy-check stub-mcp: state, record or stdio failure")
		return 1
	}
	if agyCheckHookState(args[0], d.hook) != nil {
		return fail()
	}
	// A broken stdio pipe must take the same exit-1 path as another write
	// failure, so it cannot bypass the evidence protocol via Go's SIGPIPE exit.
	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	defer signal.Stop(pipe)
	// Records are tokens, not structure: request precedes handling, and response
	// follows written reply bytes. A missing response can only lose confirmation;
	// response covers JSON-RPC errors too. B3-2 must take success from the
	// transcript, require start first, and pair each response <tag> with the
	// immediately preceding request <tag>. ANY tool request on a denied MCP row
	// is a breach even without a response; initialized is a standalone handshake
	// notification. An unpaired allowed tool request is a difference.
	record := func(kind, tag string) error { _, err := agyCheckHookClaim(args[0], kind, tag, d.hook); return err }
	if record("start", "-") != nil {
		return fail()
	}
	// Sequential bounded reads need no background reader that could survive EOF
	// or a failed output/record write. Framing is the real MCP server's JSONL.
	reader := bufio.NewScanner(d.hook.stdin)
	reader.Buffer(make([]byte, 4096), verifyLineLimit+1)
	initialized := false
	for reader.Scan() {
		req, rpcErr := parseVerifyRequest(reader.Bytes())
		tag := ""
		var result any
		if rpcErr == nil {
			if req.id == nil {
				if req.method == "notifications/initialized" {
					if record("request", "initialized") != nil {
						return fail()
					}
				}
				continue
			}
			switch req.method {
			case "initialize":
				tag = "initialize"
			case "ping":
				tag = "ping"
			case "tools/list":
				tag = "toolslist"
			case "tools/call":
				tag = "call"
				fields, err := verifyObject(req.params)
				var name string
				if err == nil && json.Unmarshal(fields["name"], &name) == nil && (name == "verify" || name == "danger") {
					tag = name
				}
			case "resources/list":
				tag = "list"
			case "resources/read":
				tag = "read"
			default:
				rpcErr = &verifyRPCError{-32601, "method not found"}
			}
			if tag != "" {
				if record("request", tag) != nil {
					return fail()
				}
				result, rpcErr = agyCheckStubHandle(req, tag, &initialized)
			}
		}
		id := req.id
		if id == nil {
			id = json.RawMessage("null")
		}
		data, err := json.Marshal(verifyResponse{"2.0", id, result, rpcErr})
		if err != nil || agyCheckHookWrite(stdout, append(data, '\n')) != nil {
			return fail()
		}
		if tag != "" && record("response", tag) != nil {
			return fail()
		}
	}
	if reader.Err() != nil {
		return fail()
	}
	return 0
}

func agyCheckStubHandle(req verifyRequest, tag string, initialized *bool) (any, *verifyRPCError) {
	invalid := func(message string) (any, *verifyRPCError) { return nil, &verifyRPCError{-32602, message} }
	if tag == "ping" {
		return map[string]any{}, nil
	}
	if tag == "initialize" {
		if *initialized {
			return nil, &verifyRPCError{-32600, "already initialized"}
		}
		fields, err := verifyObject(req.params)
		version, ok := agyCheckTranscriptString(fields, "protocolVersion")
		if err != nil || !ok {
			return invalid("initialize requires string protocolVersion")
		}
		switch version {
		case "2024-11-05", "2025-03-26", "2025-06-18":
		default:
			version = "2025-06-18"
		}
		*initialized = true
		return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}}, "serverInfo": map[string]string{"name": "ribbitto-check", "version": "1"}}, nil
	}
	if !*initialized {
		return nil, &verifyRPCError{-32600, "initialize required"}
	}
	fields := map[string]json.RawMessage{}
	var err error
	if req.params != nil {
		fields, err = verifyObject(req.params)
	}
	if err != nil || !verifyMetadata(fields) {
		return invalid("invalid params")
	}
	switch tag {
	case "toolslist", "list":
		if len(fields) > 1 || len(fields) == 1 && fields["_meta"] == nil {
			return invalid("invalid list params")
		}
		if tag == "list" {
			return map[string]any{"resources": []map[string]string{{"uri": agyCheckStubURI, "name": "fixture", "mimeType": "text/plain"}}}, nil
		}
		var tools []map[string]any
		for _, name := range []string{"verify", "danger"} {
			tools = append(tools, map[string]any{"name": name, "description": "Return fixed fixture text without side effects.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
		}
		return map[string]any{"tools": tools}, nil
	case "verify", "danger":
		for name := range fields {
			if name != "name" && name != "arguments" && name != "_meta" {
				return invalid("invalid call params")
			}
		}
		if raw, ok := fields["arguments"]; ok {
			if _, err := verifyObject(raw); err != nil {
				return invalid("invalid arguments")
			}
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": "agy-check fixture response"}}, "isError": false}, nil
	case "read":
		for name := range fields {
			if name != "uri" && name != "_meta" {
				return invalid("invalid read params")
			}
		}
		var uri string
		if json.Unmarshal(fields["uri"], &uri) != nil || uri != agyCheckStubURI {
			return invalid("unknown resource")
		}
		return map[string]any{"contents": []map[string]string{{"uri": agyCheckStubURI, "mimeType": "text/plain", "text": "agy-check fixture resource"}}}, nil
	}
	return invalid("unknown fixture tool")
}
