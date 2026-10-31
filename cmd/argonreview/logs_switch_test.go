package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type implementGuardObservation struct {
	code            int
	out, diagnostic string
}

// Run the real dispatch and compare complete decisions, including refusal text.
func implementGuardLogsRegression(t *testing.T, dir, input string) implementGuardObservation {
	t.Helper()
	var base implementGuardObservation
	for _, logs := range []bool{false, true} {
		args := []string{"implement-guard", dir, "--allow-verify"}
		if logs {
			args = append(args, "--logs")
		}
		var out, diagnostic bytes.Buffer
		code := run(args, strings.NewReader(input), &out, &diagnostic)
		got := implementGuardObservation{code, out.String(), diagnostic.String()}
		if logs && got != base {
			t.Fatalf("logs changed guard decision: off=%+v on=%+v", base, got)
		}
		base = got
	}
	return base
}

func implementLogInput(raw string) string {
	return strings.Replace(implementVerifyInput(raw), `"ToolName":"verify"`, `"ToolName":"verify-log"`, 1)
}

func TestImplementGuardLogsRegression(t *testing.T) {
	_, dir := implementGuardFixture(t)
	for _, check := range append(verifyCheckNames(), verifyBrowserCheck) {
		t.Run(check, func(t *testing.T) {
			args := map[string]string{"check": check, "package": "./..."}
			if check == verifyBrowserCheck {
				args["package"], args["run"] = "./internal/web", "TestBrowser/quote"
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			got := implementGuardLogsRegression(t, dir, implementVerifyInput(string(raw)))
			implementGuardResult(t, got.code, got.out, got.diagnostic, "")
		})
	}
}

func TestImplementGuardLogsAdmission(t *testing.T) {
	_, dir := implementGuardFixture(t)
	valid := string(verifyLogArgsJSON(strings.Repeat("a", verifyLogIDBytes*2), "index", "", "1"))
	rows := []struct {
		name, raw string
		valid     bool
	}{
		{"valid", valid, true},
		{"test", string(verifyLogArgsJSON(strings.Repeat("b", verifyLogIDBytes*2), "test", "#1", "1")), true},
		{"empty", `{}`, false}, {"null", `null`, false},
		{"verify arguments", `{"check":"test","package":"./..."}`, false},
		{"test name is not capture ID", strings.Replace(valid, strings.Repeat("a", verifyLogIDBytes*2), "TestBrowser/quote", 1), false},
		{"extra", strings.TrimSuffix(valid, "}") + `,"extra":"x"}`, false},
		{"duplicate", strings.TrimSuffix(valid, "}") + `,"start":"1"}`, false},
		{"missing name", string(verifyLogArgsJSON(strings.Repeat("a", verifyLogIDBytes*2), "test", "", "1")), false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for _, mode := range []struct{ verify, logs bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
				t.Run(fmt.Sprint(mode), func(t *testing.T) {
					args := []string{"implement-guard", dir}
					if mode.verify {
						args = append(args, "--allow-verify")
					}
					if mode.logs {
						args = append(args, "--logs")
					}
					var out, diagnostic bytes.Buffer
					code := run(args, strings.NewReader(implementLogInput(row.raw)), &out, &diagnostic)
					if mode.logs && !mode.verify {
						if code != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "usage:") {
							t.Fatalf("logs without verify: %d %s %s", code, &out, &diagnostic)
						}
						// Also enforce the conjunction below dispatch.
						code = implementGuardMode(dir, false, true, strings.NewReader(implementLogInput(row.raw)), &out, &bytes.Buffer{}, filepath.WalkDir, os.Lstat)
						var decision struct{ Decision string }
						if err := json.Unmarshal(out.Bytes(), &decision); err != nil || code != 2 || decision.Decision != "deny" {
							t.Fatalf("guard admitted without verify: %d %s %v", code, &out, err)
						}
						return
					}
					reason := "call_mcp_tool is not allowed in the implementer trial"
					if mode.verify && mode.logs && row.valid {
						reason = ""
					}
					implementGuardResult(t, code, out.String(), diagnostic.String(), reason)
					if !mode.logs {
						var unknown, unknownLog bytes.Buffer
						unknownInput := strings.Replace(implementLogInput(row.raw), `"ToolName":"verify-log"`, `"ToolName":"unknown"`, 1)
						otherCode := run(args, strings.NewReader(unknownInput), &unknown, &unknownLog)
						if code != otherCode || !bytes.Equal(out.Bytes(), unknown.Bytes()) || !bytes.Equal(diagnostic.Bytes(), unknownLog.Bytes()) {
							t.Fatal("off refusal differs from unknown tool")
						}
					}
				})
			}
		})
	}
	// The envelope restrictions must apply to the new tool too.
	for _, row := range implementVerifyInvalidCalls(implementVerifyInput(valid)) {
		t.Run("envelope/"+row.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := run([]string{"implement-guard", dir, "--allow-verify", "--logs"}, strings.NewReader(strings.ReplaceAll(row.input, `"verify"`, `"verify-log"`)), &out, &diagnostic)
			implementGuardResult(t, code, out.String(), diagnostic.String(), "call_mcp_tool is not allowed in the implementer trial")
		})
	}
	for _, key := range []string{"toolAction", "toolSummary"} {
		for _, row := range []struct {
			name  string
			value any
			allow bool
		}{
			{"bounded", strings.Repeat("a", maxVerifyMetadataBytes), true},
			{"over", strings.Repeat("a", maxVerifyMetadataBytes+1), false},
			{"null", nil, false}, {"object", map[string]string{}, false},
		} {
			t.Run(key+"/"+row.name, func(t *testing.T) {
				input := implementGuardInput(t, "call_mcp_tool", map[string]any{"ServerName": verifyServerName, "ToolName": verifyLogToolName, "Arguments": json.RawMessage(valid), key: row.value})
				var out, diagnostic bytes.Buffer
				code := run([]string{"implement-guard", dir, "--allow-verify", "--logs"}, strings.NewReader(input), &out, &diagnostic)
				reason := "call_mcp_tool is not allowed in the implementer trial"
				if row.allow {
					reason = ""
				}
				implementGuardResult(t, code, out.String(), diagnostic.String(), reason)
			})
		}
	}

}

func TestImplementConfigLogsBytes(t *testing.T) {
	for _, browser := range []string{"", "/trusted/browser-mode"} {
		t.Run("browser="+browser, func(t *testing.T) {
			offH, offS, offM, err := implementConfigFiles(true, false, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", browser)
			if err != nil {
				t.Fatal(err)
			}
			onH, onS, onM, err := implementConfigFiles(true, true, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", browser)
			if err != nil {
				t.Fatal(err)
			}
			// Removing the one specified addition must restore every original byte.
			if !bytes.Equal(bytes.Replace(onH, []byte(" --allow-verify --logs"), []byte(" --allow-verify"), 1), offH) {
				t.Fatal("hooks differ beyond --logs suffix")
			}
			if !bytes.Equal(bytes.Replace(onS, []byte(",\n      \"mcp(ribbitto-verify/verify-log)\""), nil, 1), offS) {
				t.Fatal("settings differ beyond one allow entry")
			}
			suffix := []byte(",\n        \"--logs\"")
			if !bytes.Equal(bytes.Replace(onM, suffix, nil, 1), offM) {
				t.Fatal("MCP differs beyond trailing --logs")
			}
			var config struct {
				Servers map[string]struct{ Args []string } `json:"mcpServers"`
			}
			if err := json.Unmarshal(onM, &config); err != nil {
				t.Fatal(err)
			}
			args := config.Servers[verifyServerName].Args
			if args[len(args)-1] != "--logs" {
				t.Fatalf("logs is not trailing: %v", args)
			}
			for _, row := range []struct {
				name string
				got  []byte
			}{{"hooks", onH}, {"settings", onS}, {"mcp-config", onM}} {
				name := row.name + "-logs-on.json"
				if browser != "" && row.name == "mcp-config" {
					name = "mcp-config-browser-logs-on.json"
				}
				if !bytes.Equal(row.got, implementVerifyGolden(t, name)) {
					t.Fatalf("golden %s differs: %s", name, row.got)
				}
			}
			if !bytes.Equal(offH, implementVerifyGolden(t, "hooks-on.json")) || !bytes.Equal(offS, implementVerifyGolden(t, "settings-on.json")) {
				t.Fatal("logs off changed old goldens")
			}
			wantM := implementVerifyGolden(t, "mcp-config-on.json")
			if browser != "" {
				wantM = []byte(browserMCPGolden)
			}
			if !bytes.Equal(offM, wantM) {
				t.Fatal("logs off changed MCP golden")
			}
		})
	}
	h, s, m, err := implementConfigFiles(false, false, "/trusted/bin/argonreview", "/trusted/guard", "", "", "")
	if err != nil || !bytes.Equal(h, implementVerifyGolden(t, "hooks-off.json")) || !bytes.Equal(s, implementVerifyGolden(t, "settings-off.json")) || len(m) != 0 {
		t.Fatalf("off bytes changed: %v", err)
	}
}

func TestImplementConfigLogsRequiresVerify(t *testing.T) {
	generators := []struct {
		name     string
		generate func() ([]byte, error)
	}{
		{"hooks", func() ([]byte, error) {
			return implementerHooks(false, true, "/trusted/bin/argonreview", "/trusted/guard")
		}},
		{"settings", func() ([]byte, error) { return implementerSettings(false, true) }},
		{"MCP", func() ([]byte, error) {
			return implementerMCPConfig(false, true, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", "")
		}},
	}
	for _, g := range generators {
		t.Run(g.name, func(t *testing.T) {
			if data, err := g.generate(); err == nil || len(data) != 0 {
				t.Fatalf("logs without verify generated %s: %v", data, err)
			}
		})
	}
}

func TestImplementConfigLogsMismatch(t *testing.T) {
	for _, logs := range []bool{false, true} {
		for _, browser := range []string{"", "/trusted/browser-mode"} {
			h, s, m, err := implementConfigFiles(true, logs, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", browser)
			if err != nil {
				t.Fatal(err)
			}
			otherH, otherS, otherM, err := implementConfigFiles(true, !logs, "/trusted/bin/argonreview", "/trusted/guard", "/trusted/source", "/trusted/worktree", browser)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct {
				name    string
				h, s, m []byte
				reason  string
			}{
				{"matching", h, s, m, ""},
				{"hooks", otherH, s, m, "implementer hooks do not match verify switch"},
				{"settings", h, otherS, m, "implementer settings do not match verify switch"},
				{"MCP", h, s, otherM, "implementer MCP configuration does not match verify switch"},
			} {
				t.Run(fmt.Sprintf("logs=%t/browser=%s/%s", logs, browser, row.name), func(t *testing.T) {
					err := checkImplementerConfig(true, logs, row.h, row.s, row.m, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", "/trusted/guard", browser)
					if row.reason == "" {
						if err != nil {
							t.Fatal(err)
						}
					} else if err == nil || err.Error() != row.reason {
						t.Fatalf("want %q: %v", row.reason, err)
					}
				})
			}
		}
	}
}

func TestImplementConfigLogsParsing(t *testing.T) {
	rows := []struct {
		name, state string
		suffix      []string
		valid, logs bool
	}{
		{"absent", "on", nil, true, false}, {"on", "on", []string{"--logs", "on"}, true, true}, {"off", "on", []string{"--logs", "off"}, true, false},
		{"verify off on", "off", []string{"--logs", "on"}, false, false}, {"verify off off", "off", []string{"--logs", "off"}, false, false},
		{"repeated", "on", []string{"--logs", "on", "--logs", "off"}, false, false}, {"empty", "on", []string{"--logs", ""}, false, false},
		{"missing", "on", []string{"--logs"}, false, false}, {"invalid", "on", []string{"--logs", "ON"}, false, false},
		{"boolean", "on", []string{"--logs", "true"}, false, false}, {"space", "on", []string{"--logs", "on "}, false, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newImplementConfigFixture(t, row.state == "on")
			t.Run("config", func(t *testing.T) {
				c, err := parseImplementConfig(append(f.args(), row.suffix...))
				if (err == nil) != row.valid || err == nil && c.verifyLogs != row.logs {
					t.Fatalf("config: %+v %v", c, err)
				}
			})
			x := newImplementRun(t)
			if row.state == "on" {
				featureOn(t, x)
			}
			t.Run("run", func(t *testing.T) {
				r, err := implementRunArguments(append(append([]string{}, x.args...), row.suffix...), io.Discard, x.deps)
				if (err == nil) != row.valid || err == nil && r.verifyLogs != row.logs {
					t.Fatalf("run: %+v %v", r, err)
				}
			})
			t.Run("check", func(t *testing.T) {
				args := featureFlag(x.f.args(), "verify", row.state)
				if row.state == "on" {
					args = append(args, "--trusted-source", f.c.source)
				}
				// Stop after parsing so filesystem diagnostics cannot hide a flag rejection.
				args = featureFlag(args, "home", "<absent>")
				var diagnostic bytes.Buffer
				code := implementCheckCommand(append(args, row.suffix...), io.Discard, &diagnostic, x.f.deps)
				if code != 2 || strings.Contains(diagnostic.String(), "--home, --worktree") != row.valid {
					t.Fatalf("check: %d %s", code, &diagnostic)
				}
			})
		})
	}
}

func TestImplementRunLogsChecks(t *testing.T) {
	for _, logs := range []string{"on", "off"} {
		t.Run(logs, func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			x.set("logs", logs)
			x.set("browser-mode", "/trusted/browser-mode")
			check := x.deps.check
			var states []string
			x.deps.check = func(args []string, out, diagnostic io.Writer) int {
				values := map[string]string{}
				for i := 0; i < len(args); i += 2 {
					values[args[i]] = args[i+1]
				}
				state := values["--verify"]
				states = append(states, state)
				if state == "on" {
					if values["--logs"] != logs || values["--browser-mode"] != "/trusted/browser-mode" {
						t.Fatalf("check lost trusted flags: %v", args)
					}
				} else if _, ok := values["--logs"]; ok {
					t.Fatalf("idle check passed logs: %v", args)
				}
				return check(args, out, diagnostic)
			}
			x.run(t, 0)
			if !reflect.DeepEqual(states, []string{"off", "on", "on", "off"}) {
				t.Fatalf("checks did not cover initial, pre, post and idle: %v", states)
			}
		})
	}
}

func TestImplementCheckLogs(t *testing.T) {
	for _, logs := range []bool{false, true} {
		t.Run(fmt.Sprint(logs), func(t *testing.T) {
			x := newImplementRun(t)
			featureOn(t, x)
			value := "off"
			if logs {
				value = "on"
			}
			x.set("logs", value)
			r, err := implementRunArguments(x.args, io.Discard, x.deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.validate(); err != nil {
				t.Fatal(err)
			}
			for _, file := range r.files()[:3] {
				write(t, file.path, string(file.data))
			}
			args := featureFlag(x.f.args(), "verify", "on")
			args = append(args, "--trusted-source", r.source, "--logs", value)
			var out bytes.Buffer
			if code := implementCheckCommand(args, &out, &out, x.f.deps); code != 0 {
				t.Fatalf("matching config: %d %s", code, &out)
			}
			opposite := "on"
			if logs {
				opposite = "off"
			}
			out.Reset()
			if code := implementCheckCommand(featureFlag(args, "logs", opposite), &out, &out, x.f.deps); code == 0 || !strings.Contains(out.String(), "implementer hooks do not match verify switch") {
				t.Fatalf("mismatched config: %d %s", code, &out)
			}
		})
	}
}

func TestImplementConfigLogsCommand(t *testing.T) {
	f := newImplementConfigFixture(t, true)
	var diagnostic bytes.Buffer
	if code := implementConfigCommand(append(f.args(), "--logs", "on"), io.Discard, &diagnostic); code != 0 {
		t.Fatalf("config: %d %s", code, &diagnostic)
	}
	for _, name := range []string{"hooks.json", "settings.json", "mcp_config.json"} {
		got, err := os.ReadFile(filepath.Join(f.c.out, name))
		if err != nil {
			t.Fatal(err)
		}
		h, s, m, err := implementConfigFiles(true, true, f.c.binary, f.c.guardDir, f.c.source, f.c.worktree, "")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]byte{"hooks.json": h, "settings.json": s, "mcp_config.json": m}[name]
		if !bytes.Equal(got, want) {
			t.Fatalf("published %s differs", name)
		}
	}
}

func TestImplementGuardLogsUsage(t *testing.T) {
	var diagnostic bytes.Buffer
	if code := run(nil, nil, io.Discard, &diagnostic); code != 2 {
		t.Fatal(code)
	}
	for _, command := range []string{"implement-run", "implement-check", "implement-config", "implement-guard"} {
		found := false
		for _, usage := range strings.Split(diagnostic.String(), " | ") {
			if strings.HasPrefix(usage, command+" ") && strings.Contains(usage, "--logs") {
				found = true
			}
		}
		if !found {
			t.Fatalf("usage missing %s logs: %s", command, &diagnostic)
		}
	}
}

// Recording filesystem operations pins the same worktree inspection as verify.
// The transitive call check also rejects an added file or process operation that
// bypasses those injected operations, including one in an argument parser.
func TestImplementGuardLogsNoIO(t *testing.T) {
	root, dir := implementGuardFixture(t)
	var baseline []string
	for _, tool := range []string{verifyToolName, verifyLogToolName} {
		var operations []string
		walk := func(path string, fn fs.WalkDirFunc) error {
			operations = append(operations, "walk:"+path)
			return filepath.WalkDir(path, fn)
		}
		lstat := func(path string) (fs.FileInfo, error) {
			operations = append(operations, "lstat:"+path)
			return os.Lstat(path)
		}
		input := implementVerifyInput(`{"check":"test","package":"./..."}`)
		if tool == verifyLogToolName {
			input = implementLogInput(string(verifyLogArgsJSON(strings.Repeat("a", verifyLogIDBytes*2), "test", "/must/not/open", "1")))
		}
		var out, diagnostic bytes.Buffer
		code := implementGuardMode(dir, true, true, strings.NewReader(input), &out, &diagnostic, walk, lstat)
		implementGuardResult(t, code, out.String(), diagnostic.String(), "")
		if baseline == nil {
			baseline = operations
		} else if !reflect.DeepEqual(baseline, operations) {
			t.Fatalf("log decision changed worktree IO: %v / %v", baseline, operations)
		}
	}
	if !strings.Contains(strings.Join(baseline, "\n"), "walk:"+root) {
		t.Fatal("worktree not inspected")
	}
	implementGuardLogsCallBoundary(t)
}

func implementGuardLogsCallBoundary(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	functions := map[string]*ast.FuncDecl{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				functions[fn.Name.Name] = fn
			}
		}
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		fn := functions[name]
		if fn == nil {
			return
		}
		parameters := map[string]bool{}
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				parameters[name.Name] = true
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				if !parameters[callee.Name] {
					visit(callee.Name)
				}
			case *ast.SelectorExpr:
				pkg, ok := callee.X.(*ast.Ident)
				if !ok {
					break
				}
				if pkg.Name == "os" {
					if name != "implementGuardReason" || callee.Sel.Name != "ReadFile" || len(call.Args) != 1 {
						t.Fatalf("guard acquired filesystem capability in %s: os.%s", name, callee.Sel.Name)
					}
					joined, ok := call.Args[0].(*ast.CallExpr)
					if !ok || len(joined.Args) != 2 {
						t.Fatal("guard read changed")
					}
					base, ok := joined.Args[0].(*ast.Ident)
					if !ok || base.Name != "dir" {
						t.Fatal("guard marker directory changed")
					}
					marker, ok := joined.Args[1].(*ast.BasicLit)
					if !ok || marker.Value != `"implementer-worktree"` {
						t.Fatal("guard read beyond marker")
					}
				}
				if pkg.Name == "exec" || pkg.Name == "unix" || pkg.Name == "syscall" || pkg.Name == "net" {
					t.Fatalf("guard acquired process or external IO in %s", name)
				}
			}
			return true
		})
	}
	visit("implementGuardMode")
}
