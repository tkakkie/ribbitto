package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestVerifyLogMCPArguments(t *testing.T) {
	source, tree := resolved(t, t.TempDir()), resolved(t, t.TempDir())
	base := []string{"--trusted-source", source, "--worktree", tree}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"first", append([]string{"--logs"}, base...)},
		{"middle", []string{"--trusted-source", source, "--logs", "--worktree", tree}},
		{"path value", []string{"--trusted-source", "--logs", "--worktree", tree}},
		{"before browser", append(append([]string{}, base...), "--logs", "--browser-mode", "/mode")},
		{"repeat", append(append([]string{}, base...), "--logs", "--logs")},
		{"value", append(append([]string{}, base...), "--logs", "on")},
		{"equals", append(append([]string{}, base...), "--logs=on")},
		{"other", append(append([]string{}, base...), "--other")},
		{"browser then value", append(append([]string{}, base...), "--browser-mode", "/mode", "--logs", "on")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, log bytes.Buffer
			code := startVerifyMCP(context.Background(), tc.args, nil, func(string) error { t.Error("trust called for invalid usage"); return nil }, noSweep, noVerify(t), strings.NewReader(""), &out, &log)
			if code != 2 || out.Len() != 0 || log.String() != verifyMCPUsage+"\n" {
				t.Fatalf("usage refusal: %d %q %q", code, &out, &log)
			}
		})
	}
	for _, browser := range []bool{false, true} {
		for _, logs := range []bool{false, true} {
			t.Run(fmt.Sprintf("browser=%v/logs=%v", browser, logs), func(t *testing.T) {
				args := append([]string{}, base...)
				if browser {
					args = append(args, "--browser-mode", "/mode")
				}
				if logs {
					args = append(args, "--logs")
				}
				v := logRecordingVerifier{
					legacy: func(context.Context, string, string) (string, error) {
						if logs {
							t.Error("logs did not use recording interface")
						}
						return "FAILED\nold summary", nil
					},
					recorded: func(context.Context, string, string, string) (string, *containerRecord, error) {
						if !logs {
							t.Error("logs-off used recording interface")
						}
						return "FAILED\nold summary", verifyLogFixture("failure"), nil
					},
				}
				text, bad := logMCPRun(t, context.Background(), mcpCall, nil, func(ctx context.Context, in io.Reader, out, log io.Writer) int {
					return startVerifyMCP(ctx, args, nil, func(string) error { return nil }, noSweep, v, in, out, log)
				})
				if !bad || strings.Contains(text, verifyLogRunPrefix) != logs {
					t.Fatalf("startup logs state: %q", text)
				}
				if !logs && text != "FAILED\nold summary" {
					t.Fatal("logs-off bytes changed")
				}
			})
		}
	}
}

func TestVerifyLogMCPToolsListUnchanged(t *testing.T) {
	input := mcpInit + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var previous string
	for _, store := range []*verifyLogStore{nil, {}} {
		var out, log bytes.Buffer
		if code := (verifyMCPServer{verifier: noVerify(t), logs: store}).serve(context.Background(), strings.NewReader(input), &out, &log); code != 0 {
			t.Fatal(code)
		}
		lines := strings.Split(out.String(), "\n")
		if previous != "" && lines[1] != previous {
			t.Fatal("tools/list bytes depend on logs")
		}
		previous = lines[1]
	}
}
