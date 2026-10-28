package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type agyCheckRowFake struct {
	Rows                            bool
	Home, Isolation, Row, Deviation string
	Version                         string
}

type agyCheckFakeHook struct {
	Command string
	Timeout int
}

func agyCheckRowsFake(data []byte) bool {
	var f agyCheckRowFake
	if json.Unmarshal(data, &f) != nil || !f.Rows {
		return false
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		tmp := os.Getenv("TMPDIR")
		cwd, err := os.Getwd()
		if err != nil || cwd != tmp || filepath.Dir(tmp) != f.Isolation || !strings.HasPrefix(filepath.Base(tmp), "pin-") || os.Getenv("HOME") != f.Home {
			os.Exit(94)
		}
		version := f.Version
		if version == "" {
			version = "1.4.7"
		}
		fmt.Println("agy " + version)
		os.Exit(0)
	}
	if len(os.Args) != 10 || os.Args[1] != "-p" || os.Args[5] != "--sandbox" || os.Args[8] != "--output-format" || os.Args[9] != "stream-json" {
		os.Exit(80)
	}
	prompt := os.Args[2]
	field := func(name string) string {
		for _, line := range strings.Split(prompt, "\n") {
			if value, ok := strings.CutPrefix(line, name+": "); ok {
				return value
			}
		}
		return ""
	}
	id, tool, target := field("Scenario"), field("Tool"), field("Target")
	cwd, _ := os.Getwd()
	env := os.Environ()
	slices.Sort(env)
	want := []string{"HOME=" + f.Home, "PATH=" + agyCheckRunPath, "TMPDIR=" + filepath.Join(filepath.Dir(cwd), "tmp")}
	slices.Sort(want)
	if !slices.Equal(env, want) || !implementRunInside(f.Isolation, cwd) {
		os.Exit(81)
	}
	deviation := ""
	if f.Row == id {
		deviation = f.Deviation
	}
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	var hooks []agyCheckFakeHook
	load := func(path string) []agyCheckFakeHook {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var groups map[string]map[string][]struct {
			Matcher string
			Hooks   []agyCheckFakeHook
		}
		if json.Unmarshal(b, &groups) != nil {
			os.Exit(82)
		}
		var result []agyCheckFakeHook
		for _, group := range groups {
			for _, matcher := range group["PreToolUse"] {
				result = append(result, matcher.Hooks...)
			}
		}
		return result
	}
	// The six candidates are read from disk; only .agent and .agents load in
	// the recorded 1.4.7 model. Same-name groups remain independent.
	for _, location := range []string{".agent/hooks.json", ".agents/hooks.json", ".gemini/config/hooks.json", ".gemini/hooks.json", ".antigravity/hooks.json", "hooks.json"} {
		found := load(filepath.Join(cwd, location))
		if len(found) > 0 && (location == ".agent/hooks.json" || location == ".agents/hooks.json") {
			if deviation != "flip" {
				emit(map[string]any{"event": "hook_loaded", "path": location})
			}
			hooks = append(hooks, found...)
		}
	}
	hooks = append(hooks, load(filepath.Join(f.Home, ".gemini/config/hooks.json"))...)
	switch deviation {
	case "protected-load":
		emit(map[string]any{"event": "hook_loaded", "path": ".agent/new/config.json"})
	case "unprotected-load":
		emit(map[string]any{"event": "hook_loaded", "path": "new/config.json"})
	case "timeout":
		time.Sleep(time.Minute)
	case "unparsed":
		fmt.Println("malformed")
		os.Exit(0)
	}
	settings, _ := os.ReadFile(filepath.Join(f.Home, ".gemini/antigravity-cli/settings.json"))
	var permissions struct {
		Permissions struct{ Allow, Deny []string }
	}
	if json.Unmarshal(settings, &permissions) != nil {
		os.Exit(83)
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		}
	}
	mcpBytes, _ := os.ReadFile(filepath.Join(f.Home, ".gemini/config/mcp_config.json"))
	if json.Unmarshal(mcpBytes, &mcp) != nil {
		os.Exit(84)
	}
	var server *exec.Cmd
	var input io.WriteCloser
	var output *bufio.Reader
	rpcN := 0
	rpc := func(method string, params any) {
		rpcN++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcN, "method": method, "params": params})
		if _, err := input.Write(append(b, '\n')); err != nil {
			os.Exit(85)
		}
		line, err := output.ReadBytes('\n')
		if err != nil {
			os.Exit(86)
		}
		var response map[string]any
		if json.Unmarshal(line, &response) != nil || response["error"] != nil {
			os.Exit(87)
		}
	}
	if cfg, ok := mcp.MCPServers[verifyServerName]; ok && deviation != "no-stub" {
		if len(cfg.Args) != 6 || cfg.Args[5] != filepath.Join(filepath.Dir(cwd), "stub") {
			os.Exit(98)
		}
		server = exec.Command(cfg.Command, cfg.Args...)
		var err error
		input, err = server.StdinPipe()
		if err != nil {
			os.Exit(88)
		}
		pipe, err := server.StdoutPipe()
		if err != nil || server.Start() != nil {
			os.Exit(89)
		}
		output = bufio.NewReader(pipe)
		rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	}
	runHook := func(h agyCheckFakeHook) bool {
		state := filepath.Join(filepath.Dir(cwd), "state")
		if !strings.Contains(h.Command, " '"+state+"'") && h.Command != "'"+filepath.Join(state, "hook-missing")+"'" {
			os.Exit(98)
		}
		if h.Timeout != 20 {
			os.Exit(95)
		}
		// Scale only the fake's wait for the real hanging fixture. The generated
		// configuration still carries 20 seconds; no real agy is used by tests.
		limit := 5 * time.Second
		if strings.Contains(h.Command, " hook hang ") {
			limit = 250 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.Stdin = strings.NewReader(`{"tool_name":"` + tool + `","tool_input":{}}`)
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			return false
		}
		if out.Len() == 0 {
			return true
		}
		var reply struct{ Decision string }
		return json.Unmarshal(out.Bytes(), &reply) == nil && reply.Decision == "allow"
	}
	call := func(n int, batch string) map[string]any {
		allowed := true
		for i, h := range hooks {
			if deviation == "no-claim" || deviation == "one-workspace-claim" && i == 1 {
				allowed = false
				continue
			}
			if !runHook(h) {
				allowed = false
			}
		}
		if tool == "run_command" && slices.Contains(permissions.Permissions.Deny, "command(*)") {
			allowed = false
		}
		if tool == "call_mcp_tool" {
			name := "danger"
			if id == "mcp-off" {
				name = "verify"
			}
			if slices.Contains(permissions.Permissions.Deny, "mcp(*)") || !slices.Contains(permissions.Permissions.Allow, "mcp("+verifyServerName+"/"+name+")") {
				allowed = false
			}
			if deviation == "stub-response" && server != nil {
				rpc("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
			}
			if allowed && server != nil {
				rpc("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
			}
		}
		if deviation == "flip" && (id != "reconciliation" || n == 1) {
			allowed = !allowed
		}
		if allowed {
			path := target
			if id == "reconciliation" {
				path = filepath.Join(cwd, fmt.Sprintf("created-%02d", n))
			}
			if path != "" && !implementRunInside(f.Isolation, path) {
				os.Exit(98)
			}
			switch tool {
			case "write_to_file", "run_command":
				if os.WriteFile(path, []byte("fixture"), 0o600) != nil {
					os.Exit(96)
				}
			case "view_file":
				if _, err := os.ReadFile(path); err != nil {
					allowed = false
				}
			}
		}
		state := "ERROR"
		if allowed {
			state = "DONE"
		}
		return map[string]any{"event": "tool_step", "tool": tool, "state": state, "batch": batch}
	}
	if id == "reconciliation" {
		var count int
		_, _ = fmt.Sscanf(field("Calls"), "%d", &count)
		if count < 4 {
			count = 4
		}
		for batch := 0; batch < 2; batch++ {
			first, last := 1, count/2
			if batch == 1 {
				first, last = count/2+1, count
			}
			steps := make([]map[string]any, last-first+1)
			var wg sync.WaitGroup
			for n := first; n <= last; n++ {
				wg.Go(func() { steps[n-first] = call(n, fmt.Sprint(batch)) })
			}
			wg.Wait()
			for _, step := range steps {
				emit(step)
			}
		}
	} else if tool != "read_url" && tool != "execute_url" || deviation == "flip" {
		step := call(1, "")
		if tool == "list_resources" || tool == "read_resource" {
			if server != nil && step["state"] == "DONE" {
				if tool == "list_resources" {
					rpc("resources/list", map[string]any{})
				} else {
					rpc("resources/read", map[string]any{"uri": agyCheckStubURI})
				}
			} else if server == nil {
				step["state"] = "ERROR"
			}
		}
		emit(step)
		if deviation == "retry" {
			emit(step)
		}
	}
	if server != nil {
		_ = input.Close()
		if server.Wait() != nil {
			os.Exit(97)
		}
	}
	state := filepath.Join(filepath.Dir(cwd), "state/calls")
	switch deviation {
	case "wrong-tag":
		_ = os.WriteFile(filepath.Join(state, "000001"), []byte("allow wrong-tag\n"), 0o600)
	case "gap":
		_ = os.Rename(filepath.Join(state, "000002"), filepath.Join(state, "000099"))
	case "extra-file":
		_ = os.WriteFile(filepath.Join(cwd, "unexpected"), nil, 0o600)
	case "contradictory":
		_ = os.WriteFile(filepath.Join(state, "000001"), []byte("deny reconciliation\n"), 0o600)
	case "missing-response":
		stub := filepath.Join(filepath.Dir(cwd), "stub/calls")
		entries, _ := os.ReadDir(stub)
		if len(entries) > 0 {
			_ = os.Remove(filepath.Join(stub, entries[len(entries)-1].Name()))
		}
	}
	emit(map[string]any{"event": "message", "content": "fixture finished"})
	if deviation == "nonzero-no-result" {
		os.Exit(7)
	}
	emit(map[string]any{"event": "result"})
	if deviation == "nonzero-result" {
		os.Exit(7)
	}
	os.Exit(0)
	return true
}
