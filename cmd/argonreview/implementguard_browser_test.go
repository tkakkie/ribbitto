package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The recorded shapes live in Go rather than a JSON file so that no new data
// file enters the language table (decision 42).
func TestImplementGuardBrowserGolden(t *testing.T) {
	rows := []struct {
		name        string
		allowVerify bool
		input       string
		decision    string
		reason      string
		status      int
	}{
		{"allowed ./internal/web TestBrowser/drafts-markers/sidebar-sse", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/drafts-markers/sidebar-sse"}}}}`, "allow", "", 0},
		{"feature-off ./internal/web TestBrowser/drafts-markers/sidebar-sse", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/drafts-markers/sidebar-sse"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"allowed ./internal/web TestBrowser/quote/scripts=true/width=1280", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote/scripts=true/width=1280"}}}}`, "allow", "", 0},
		{"feature-off ./internal/web TestBrowser/quote/scripts=true/width=1280", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote/scripts=true/width=1280"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"allowed ./internal/web TestTopicPostBrowser", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestTopicPostBrowser"}}}}`, "allow", "", 0},
		{"feature-off ./internal/web TestTopicPostBrowser", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestTopicPostBrowser"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"allowed ./cmd/ribbitto TestBrowser/drafts-markers/sidebar-sse", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestBrowser/drafts-markers/sidebar-sse"}}}}`, "allow", "", 0},
		{"feature-off ./cmd/ribbitto TestBrowser/drafts-markers/sidebar-sse", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestBrowser/drafts-markers/sidebar-sse"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"allowed ./cmd/ribbitto TestBrowser/quote/scripts=true/width=1280", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestBrowser/quote/scripts=true/width=1280"}}}}`, "allow", "", 0},
		{"feature-off ./cmd/ribbitto TestBrowser/quote/scripts=true/width=1280", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestBrowser/quote/scripts=true/width=1280"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"allowed ./cmd/ribbitto TestTopicPostBrowser", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestTopicPostBrowser"}}}}`, "allow", "", 0},
		{"feature-off ./cmd/ribbitto TestTopicPostBrowser", false, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./cmd/ribbitto","run":"TestTopicPostBrowser"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"bad-package", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./...","run":"TestBrowser/quote"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"bad-run", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"missing-run", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"extra-argument", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote","extra":"x"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"wrong-case", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","Run":"TestBrowser/quote"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"ordinary-run", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test","package":"./...","run":"TestBrowser/quote"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"other-server", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"other","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"other-tool", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"other","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote"}}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
		{"extra-MCP-argument", true, `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":{"check":"test-browser","package":"./internal/web","run":"TestBrowser/quote"},"extra":"x"}}}`, "deny", "call_mcp_tool is not allowed in the implementer trial", 2},
	}
	if len(rows) != 21 {
		t.Fatalf("browser guard table must retain 21 cases: %d", len(rows))
	}
	_, dir := implementGuardFixture(t)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			args := []string{"implement-guard", dir}
			if row.allowVerify {
				args = append(args, "--allow-verify")
			}
			var out, log bytes.Buffer
			code := run(args, strings.NewReader(row.input), &out, &log)
			var got struct{ Decision, Reason string }
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if code != row.status || got.Decision != row.decision || got.Reason != row.reason {
				t.Fatalf("browser guard scope %s: code=%d decision=%q reason=%q; want %d %q %q", row.name, code, got.Decision, got.Reason, row.status, row.decision, row.reason)
			}
		})
	}
}
