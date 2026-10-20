package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func implementVerifyInput(arguments string) string {
	return `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify","Arguments":` + arguments + `}}}`
}

func implementVerifyCheck(t *testing.T, dir, input string, allow bool) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := run([]string{"implement-guard", dir, "--allow-verify"}, strings.NewReader(input), &out, &stderr)
	want := "call_mcp_tool is not allowed in the implementer trial"
	if allow {
		want = ""
	}
	implementGuardResult(t, code, out.String(), stderr.String(), want)
}

// This table is shared by the guard and server tests, with explicit expected
// decisions so agreement alone cannot let two permissive parsers pass.
func implementVerifyArgumentsCases() []struct {
	name, raw string
	allow     bool
} {
	cases := []struct {
		name, raw string
		allow     bool
	}{
		{"null", `null`, false}, {"array", `[]`, false}, {"number", `1`, false},
		{"bool", `true`, false}, {"JSON string", `"{\"check\":\"test\",\"package\":\"./...\"}"`, false},
		{"empty", `{}`, false}, {"missing check", `{"package":"./..."}`, false},
		{"missing package", `{"check":"test"}`, false},
		{"extra", `{"check":"test","package":"./...","extra":"x"}`, false},
		{"duplicate", `{"check":"build","check":"test","package":"./..."}`, false},
		{"escaped duplicate", `{"check":"test","package":"./...","p\u0061ckage":"./..."}`, false},
		{"case check key", `{"Check":"test","package":"./..."}`, false},
		{"case package key", `{"check":"test","Package":"./..."}`, false},
		{"package at length limit", `{"check":"test","package":"./` + strings.Repeat("a", 198) + `"}`, true},
	}
	for _, check := range []string{"build", "vet", "test"} {
		for _, pattern := range []string{".", "./...", "./cmd/argonreview", "./cmd/...", "./a_b/c-d"} {
			cases = append(cases, struct {
				name, raw string
				allow     bool
			}{check + " " + pattern, fmt.Sprintf(`{"check":%q,"package":%q}`, check, pattern), true})
		}
	}
	for _, key := range []string{"check", "package"} {
		values := []string{`null`, `1`, `true`, `[]`, `{}`, `""`, `"-x"`, `"/tmp/x"`, `".."`, `"./a/../b"`}
		badStrings := []string{"test ", "TEST", "./x.go", "a/b", "./a//b", "./a/", `C:\x`, "~/x", "file:///tmp/x", "./a b", "./a\nb", strings.Repeat("x", 201), "./" + strings.Repeat("a", 199)}
		for _, value := range badStrings {
			encoded, _ := json.Marshal(value)
			values = append(values, string(encoded))
		}
		for _, value := range values {
			check, pattern := `"test"`, `"./..."`
			if key == "check" {
				check = value
			} else {
				pattern = value
			}
			cases = append(cases, struct {
				name, raw string
				allow     bool
			}{key + "=" + value, `{"check":` + check + `,"package":` + pattern + `}`, false})
		}
	}
	return cases
}

func TestImplementVerifySharedSchema(t *testing.T) {
	_, dir := implementGuardFixture(t)
	for _, tc := range implementVerifyArgumentsCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseVerifyCall([]byte(`{"name":"verify","arguments":` + tc.raw + `}`))
			if (err == nil) != tc.allow {
				t.Errorf("server accepted=%v, want %v: %v", err == nil, tc.allow, err)
			}
			implementVerifyCheck(t, dir, implementVerifyInput(tc.raw), tc.allow)
		})
	}
}

func TestImplementVerifyGuard(t *testing.T) {
	_, dir := implementGuardFixture(t)
	valid := implementVerifyInput(`{"check":"test","package":"./cmd/argonreview"}`)
	implementVerifyCheck(t, dir, valid, true)
	implementGuardCheck(t, dir, valid, "call_mcp_tool is not allowed in the implementer trial")
	cases := []struct{ name, input string }{
		{"danger", strings.Replace(valid, `"ToolName":"verify"`, `"ToolName":"danger"`, 1)},
		{"other server", strings.Replace(valid, `"ribbitto-verify"`, `"other"`, 1)},
		{"envelope duplicate", strings.TrimSuffix(valid, "}") + `,"toolCall":{"name":"call_mcp_tool","args":{}}}`},
		{"envelope duplicate last valid", `{"toolCall":{},` + valid[1:]},
		{"toolCall duplicate", strings.Replace(valid, `"name":"call_mcp_tool"`, `"name":"danger","name":"call_mcp_tool"`, 1)},
		{"args duplicate", strings.Replace(valid, `"ToolName":"verify"`, `"ToolName":"danger","ToolName":"verify"`, 1)},
		{"escaped duplicate", strings.Replace(valid, `"ToolName":"verify"`, `"ToolName":"danger","ToolN\u0061me":"verify"`, 1)},
		{"unused nested duplicate", strings.TrimSuffix(valid, "}") + `,"unused":[{"x":0,"x":1}]}`},
		{"extra arg", strings.Replace(valid, `"ServerName":`, `"extra":0,"ServerName":`, 1)},
		{"missing server", strings.Replace(valid, `"ServerName":"ribbitto-verify",`, "", 1)},
		{"missing tool", strings.Replace(valid, `"ToolName":"verify",`, "", 1)},
		{"missing Arguments", `{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"ribbitto-verify","ToolName":"verify"}}}`},
		{"no args", `{"toolCall":{"name":"call_mcp_tool"}}`},
		{"empty args", `{"toolCall":{"name":"call_mcp_tool","args":{}}}`},
		{"null args", `{"toolCall":{"name":"call_mcp_tool","args":null}}`},
		{"string args", `{"toolCall":{"name":"call_mcp_tool","args":"x"}}`},
		{"array args", `{"toolCall":{"name":"call_mcp_tool","args":[]}}`},
	}
	for _, field := range []string{"ServerName", "ToolName"} {
		value := "verify"
		if field == "ServerName" {
			value = "ribbitto-verify"
		}
		for _, other := range []string{strings.ToUpper(value), " " + value, value + " ", value + "-extra"} {
			cases = append(cases, struct{ name, input string }{field + "=" + other,
				strings.Replace(valid, fmt.Sprintf(`%q:%q`, field, value), fmt.Sprintf(`%q:%q`, field, other), 1)})
		}
		for _, raw := range []string{`null`, `1`, `true`, `[]`, `{}`} {
			cases = append(cases, struct{ name, input string }{field + "=" + raw,
				strings.Replace(valid, fmt.Sprintf(`%q:%q`, field, value), fmt.Sprintf(`%q:`, field)+raw, 1)})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { implementVerifyCheck(t, dir, tc.input, false) })
	}
	for _, tool := range []string{"manage_task", "list_resources", "read_resource", "run_command", "danger"} {
		t.Run(tool, func(t *testing.T) {
			var out, stderr bytes.Buffer
			input := strings.Replace(valid, `"name":"call_mcp_tool"`, fmt.Sprintf(`"name":%q`, tool), 1)
			code := run([]string{"implement-guard", dir, "--allow-verify"}, strings.NewReader(input), &out, &stderr)
			implementGuardResult(t, code, out.String(), stderr.String(), tool+" is not allowed in the implementer trial")
		})
	}
}

// The recording contains toolCall itself, not the hook's stdin envelope.
func implementVerifyRealInput(t *testing.T, line int, check string) string {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(implementVerifyGolden(t, "real-agy-calls.jsonl")), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("recorded %d calls, want 4", len(lines))
	}
	var call struct {
		Args map[string]json.RawMessage `json:"args"`
		Name string                     `json:"name"`
	}
	if err := json.Unmarshal(lines[line-1], &call); err != nil {
		t.Fatal(err)
	}
	if call.Name == "call_mcp_tool" {
		call.Args["ServerName"] = json.RawMessage(`"ribbitto-verify"`)
	}
	if line == 1 {
		call.Args["ToolName"] = json.RawMessage(`"verify"`)
		call.Args["Arguments"] = json.RawMessage(fmt.Sprintf(`{"check":%q,"package":"./cmd/argonreview"}`, check))
	}
	data, err := json.Marshal(struct {
		ToolCall any `json:"toolCall"`
	}{call})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestImplementVerifyRealInput(t *testing.T) {
	_, dir := implementGuardFixture(t)
	for _, check := range []string{"build", "vet", "test"} {
		t.Run(check, func(t *testing.T) {
			input := implementVerifyRealInput(t, 1, check)
			implementVerifyCheck(t, dir, input, true)
			implementGuardCheck(t, dir, input, "call_mcp_tool is not allowed in the implementer trial")
		})
	}
	for _, line := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("recorded line %d", line), func(t *testing.T) {
			input := implementVerifyRealInput(t, line, "test")
			var out, stderr bytes.Buffer
			code := run([]string{"implement-guard", dir, "--allow-verify"}, strings.NewReader(input), &out, &stderr)
			tool := "call_mcp_tool"
			if line == 4 {
				tool = "manage_task"
			}
			implementGuardResult(t, code, out.String(), stderr.String(), tool+" is not allowed in the implementer trial")
		})
	}
}

func TestImplementVerifyMetadata(t *testing.T) {
	_, dir := implementGuardFixture(t)
	valid := implementVerifyRealInput(t, 1, "test")
	var envelope struct {
		ToolCall struct {
			Args map[string]json.RawMessage `json:"args"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal([]byte(valid), &envelope); err != nil {
		t.Fatal(err)
	}
	// Preserve the recorded fields and wrap them through the existing helper.
	input := func(remove []string, add map[string]json.RawMessage) string {
		t.Helper()
		args := make(map[string]json.RawMessage)
		for key, value := range envelope.ToolCall.Args {
			args[key] = value
		}
		for _, key := range remove {
			delete(args, key)
		}
		for key, value := range add {
			args[key] = value
		}
		return implementGuardInput(t, "call_mcp_tool", args)
	}
	for _, remove := range [][]string{nil, {"toolAction"}, {"toolSummary"}, {"toolAction", "toolSummary"}} {
		t.Run(fmt.Sprintf("remove %v", remove), func(t *testing.T) {
			implementVerifyCheck(t, dir, input(remove, nil), true)
		})
	}
	for _, key := range []string{"extra", "toolaction", "ToolAction", "toolsummary", "ToolSummary", "toolSummary2"} {
		t.Run("unknown "+key, func(t *testing.T) {
			implementVerifyCheck(t, dir, input(nil, map[string]json.RawMessage{key: json.RawMessage(`"x"`)}), false)
		})
	}
	for _, key := range []string{"toolAction", "toolSummary"} {
		for _, raw := range []string{`null`, `0`, `{}`, `[]`, `true`, `false`} {
			t.Run(key+"="+raw, func(t *testing.T) {
				implementVerifyCheck(t, dir, input(nil, map[string]json.RawMessage{key: json.RawMessage(raw)}), false)
			})
		}
		// Escapes and multibyte text ensure the cap counts decoded bytes.
		for _, tc := range []struct {
			name, raw string
			allow     bool
		}{
			{"empty", `""`, true},
			{"limit", `"` + strings.Repeat("a", 1024) + `"`, true},
			{"over", `"` + strings.Repeat("a", 1025) + `"`, false},
			{"escaped limit", `"` + strings.Repeat(`\u0061`, 1024) + `"`, true},
			{"multibyte over", `"` + strings.Repeat("é", 513) + `"`, false},
		} {
			t.Run(key+" "+tc.name, func(t *testing.T) {
				implementVerifyCheck(t, dir, input(nil, map[string]json.RawMessage{key: json.RawMessage(tc.raw)}), tc.allow)
			})
		}
		for _, duplicate := range []string{key, strings.Replace(key, "tool", `to\u006fl`, 1)} {
			t.Run("duplicate "+duplicate, func(t *testing.T) {
				bad := strings.Replace(valid, `"`+key+`":`, `"`+duplicate+`":"duplicate","`+key+`":`, 1)
				implementVerifyCheck(t, dir, bad, false)
			})
		}
	}
	for _, key := range []string{"ServerName", "ToolName", "Arguments"} {
		t.Run("missing "+key, func(t *testing.T) {
			implementVerifyCheck(t, dir, input([]string{key}, nil), false)
		})
	}
}

func TestImplementVerifyPrechecks(t *testing.T) {
	valid := implementVerifyInput(`{"check":"test","package":"./..."}`)
	for _, tc := range []struct{ name, input, reason string }{
		{"trailing JSON", valid + `{}`, "guard error: unreadable hook input"},
		{"size", valid + strings.Repeat(" ", maxGuardInput), "hook input too large"},
		{"UTF8", strings.TrimSuffix(valid, "}") + ",\"unused\":\"\xff\"}", "guard error: invalid Unicode input"},
		{"surrogate", strings.TrimSuffix(valid, "}") + `,"unused":"\ud800"}`, "guard error: invalid Unicode input"},
		{"depth", strings.TrimSuffix(valid, "}") + `,"unused":` + strings.Repeat("[", 512) + "0" + strings.Repeat("]", 512) + "}", "guard error: JSON exceeds safe parsing limits"},
		{"integer", strings.TrimSuffix(valid, "}") + `,"unused":` + strings.Repeat("1", 4301) + "}", "guard error: JSON exceeds safe parsing limits"},
		{"symlink", valid, "the worktree holds a symlink"},
		{"broken worktree", valid, "worktree path is not a clean real path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := implementGuardFixture(t)
			if tc.name == "symlink" {
				if err := os.Symlink("a.go", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "broken worktree" {
				implementGuardWrite(t, filepath.Join(dir, "implementer-worktree"), "relative")
			}
			var out, stderr bytes.Buffer
			code := run([]string{"implement-guard", dir, "--allow-verify"}, strings.NewReader(tc.input), &out, &stderr)
			implementGuardResult(t, code, out.String(), stderr.String(), tc.reason)
		})
	}
}

func TestImplementVerifyRecorded(t *testing.T) {
	var cases []struct {
		Case, Tool, Decision, Reason string
		Args                         json.RawMessage
		Exit                         int
		Symlink                      bool
	}
	if err := json.Unmarshal(implementVerifyGolden(t, "cases.json"), &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 33 {
		t.Fatalf("recorded %d cases, want 33", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Case, func(t *testing.T) {
			root, dir := implementGuardFixture(t)
			if tc.Symlink {
				if err := os.Symlink("a.go", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			quoted, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			args := json.RawMessage(strings.ReplaceAll(string(tc.Args), "<W>", string(quoted[1:len(quoted)-1])))
			var out, stderr bytes.Buffer
			code := run([]string{"implement-guard", dir, "--allow-verify"}, strings.NewReader(implementGuardInput(t, tc.Tool, args)), &out, &stderr)
			implementGuardResult(t, code, out.String(), stderr.String(), strings.ReplaceAll(tc.Reason, "<W>", root))
			var decision map[string]string
			if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
				t.Fatal(err)
			}
			if code != tc.Exit || decision["decision"] != tc.Decision {
				t.Fatalf("exit %d, decision %v; recorded %d, %s", code, decision, tc.Exit, tc.Decision)
			}
		})
	}
}

func implementVerifyGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "implement-guard", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImplementVerifySettings(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			name := "settings-off.json"
			if on {
				name = "settings-on.json"
			}
			got := implementerSettings(on)
			if !bytes.Equal(got, implementVerifyGolden(t, name)) {
				t.Fatalf("settings differ from %s: %s", name, got)
			}
			if !on && (len(got) != 171 || fmt.Sprintf("%x", sha256.Sum256(got)) != "3829ca04f37cbae281819021d98c9e144effe9fa6171f10636beb38eac8d2b0c") {
				t.Fatal("trusted off settings changed")
			}
			var settings struct {
				Permissions struct{ Allow, Deny []string }
			}
			if err := json.Unmarshal(got, &settings); err != nil {
				t.Fatal(err)
			}
			deny := strings.Join(settings.Permissions.Deny, ",")
			want := "command(*),read_url(*),execute_url(*)"
			if !on {
				want += ",mcp(*)"
			}
			allow := "write_file(*)"
			if on {
				allow += ",mcp(ribbitto-verify/verify)"
			}
			if deny != want || strings.Join(settings.Permissions.Allow, ",") != allow || on && bytes.Contains(got, []byte("mcp(*)")) {
				t.Fatal("permission rules changed")
			}
		})
	}
	if !strings.Contains(settingsJSON, `"mcp(*)"`) || strings.Contains(settingsJSON, `"allow"`) {
		t.Fatal("review settings must retain their blanket deny and no allows")
	}
}

func TestImplementVerifyHooks(t *testing.T) {
	paths := []string{"/trusted/bin/argonreview", "/trusted/guard"}
	for _, verify := range []bool{false, true} {
		name := "hooks-off.json"
		if verify {
			name = "hooks-on.json"
		}
		got, err := implementerHooks(verify, paths[0], paths[1])
		if err != nil || !bytes.Equal(got, implementVerifyGolden(t, name)) {
			t.Fatalf("hooks differ from %s: %s, %v", name, got, err)
		}
		for i := range paths {
			for _, invalid := range []string{"", "relative", "/trusted/../other", "/trusted//other", "/trusted/./other", "/trusted/", "/trusted/quo'te", "/trusted/new\nline"} {
				t.Run(fmt.Sprintf("verify=%t/path%d=%q", verify, i, invalid), func(t *testing.T) {
					bad := append([]string(nil), paths...)
					bad[i] = invalid
					got, err := implementerHooks(verify, bad[0], bad[1])
					if err == nil || len(got) != 0 {
						t.Fatalf("invalid hook path accepted: %s, %v", got, err)
					}
					if strings.Contains(err.Error(), invalid) && invalid != "" {
						t.Fatal("hook error includes a path")
					}
					if err := checkImplementerConfig(verify, nil, implementerSettings(verify), nil, bad[0], "/trusted/source", "/trusted/worktree", bad[1]); err == nil {
						t.Fatal("consistency check swallowed invalid hook path")
					}
				})
			}
		}
	}
	// These paths are valid shell arguments only when quoted as whole strings.
	for _, path := range []string{"/trusted/a b", "/trusted/$(touch x);`echo x`", "/trusted/a\tb"} {
		got, err := implementerHooks(false, path, path)
		if err != nil {
			t.Fatal(err)
		}
		var hooks map[string]struct {
			PreToolUse []struct {
				Hooks []struct{ Command string }
			}
		}
		if err := json.Unmarshal(got, &hooks); err != nil {
			t.Fatal(err)
		}
		if command := hooks["ribbitto-implementer-guard"].PreToolUse[0].Hooks[0].Command; command != "'"+path+"' implement-guard '"+path+"'" {
			t.Fatalf("paths are not single-quoted: %q", command)
		}
	}
}

func TestImplementVerifyMCPConfig(t *testing.T) {
	paths := []string{"/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree"}
	got, err := implementerMCPConfig(true, paths[0], paths[1], paths[2])
	if err != nil || !bytes.Equal(got, implementVerifyGolden(t, "mcp-config-on.json")) {
		t.Fatalf("on config %s, error %v", got, err)
	}
	for i := range paths {
		for _, invalid := range []string{"", "relative", "/trusted/../other", "/trusted//other", "/trusted/./other", "/trusted/"} {
			t.Run(fmt.Sprintf("path%d=%s", i, invalid), func(t *testing.T) {
				bad := append([]string(nil), paths...)
				bad[i] = invalid
				got, err := implementerMCPConfig(true, bad[0], bad[1], bad[2])
				if err == nil || len(got) != 0 {
					t.Fatalf("invalid path accepted: %s, %v", got, err)
				}
				if err := checkImplementerConfig(true, implementVerifyGolden(t, "hooks-on.json"), implementerSettings(true), nil, bad[0], bad[1], bad[2], "/trusted/guard"); err == nil {
					t.Fatal("consistency check swallowed invalid path")
				}
				got, err = implementerMCPConfig(false, bad[0], bad[1], bad[2])
				if err != nil || len(got) != 0 {
					t.Fatalf("off config must be empty even without paths: %s, %v", got, err)
				}
			})
		}
	}
}

func TestImplementVerifyConsistency(t *testing.T) {
	off, on := implementVerifyGolden(t, "settings-off.json"), implementVerifyGolden(t, "settings-on.json")
	mcp := implementVerifyGolden(t, "mcp-config-on.json")
	type variant struct {
		name string
		data []byte
	}
	replace := func(data []byte, old, next string) []byte { return []byte(strings.Replace(string(data), old, next, 1)) }
	hooksOff, hooksOn := implementVerifyGolden(t, "hooks-off.json"), implementVerifyGolden(t, "hooks-on.json")
	hooks := []variant{
		{"off", hooksOff}, {"on", hooksOn},
		{"missing newline", bytes.TrimSuffix(hooksOff, []byte("\n"))}, {"empty", nil},
		{"wrong binary", replace(hooksOff, "/trusted/bin/argonreview", "/other/bin/argonreview")},
		{"wrong guard dir", replace(hooksOff, "/trusted/guard", "/other/guard")},
		{"Python command", replace(hooksOff, "'/trusted/bin/argonreview' implement-guard '/trusted/guard'", "python3 '/trusted/guard/implement-guard.py'")},
		{"moved flag", replace(hooksOn, "implement-guard '/trusted/guard' --allow-verify", "implement-guard --allow-verify '/trusted/guard'")},
		{"doubled flag", replace(hooksOn, " --allow-verify", " --allow-verify --allow-verify")},
		{"timeout", replace(hooksOff, `"timeout": 20`, `"timeout": 10`)},
		{"matcher", replace(hooksOff, `"matcher": "*"`, `"matcher": "call_mcp_tool"`)},
	}
	settings := []variant{
		{"off", off}, {"on", on},
		{"wildcard allow", replace(on, "mcp(ribbitto-verify/verify)", "mcp(*)")},
		{"server wildcard", replace(on, "mcp(ribbitto-verify/verify)", "mcp(ribbitto-verify/*)")},
		{"deny and allow", replace(on, `"execute_url(*)"`, `"execute_url(*)", "mcp(*)"`)},
		{"missing newline", bytes.TrimSuffix(off, []byte("\n"))}, {"empty", nil},
		{"extra allow", replace(on, `"write_file(*)",`, `"write_file(*)", "read_url(*)",`)},
	}
	configs := []variant{
		{"empty", nil}, {"on", mcp},
		{"wrong binary", replace(mcp, "/trusted/bin/argonreview", "/other/bin/argonreview")},
		{"wrong server", replace(mcp, "ribbitto-verify", "other")},
		{"no env -i", replace(mcp, "        \"-i\",\n", "")},
		{"extra environment", replace(mcp, `"PATH=/usr/bin:/bin",`, `"PATH=/usr/bin:/bin", "EXTRA=value",`)},
		{"extra server", replace(mcp, `"mcpServers": {`, `"mcpServers": {"other": {},`)},
		{"extra argument", replace(mcp, `"verify-mcp",`, `"verify-mcp", "extra",`)},
		{"whitespace", replace(mcp, "  ", " ")},
		{"key order", replace(replace(mcp, `"args": [`, `"command": "/usr/bin/env", "args": [`), "      \"command\": \"/usr/bin/env\",\n", "")},
		{"disabled", replace(mcp, `"disabled": false`, `"disabled": true`)},
	}
	for _, variants := range [][]variant{hooks, settings, configs} {
		for _, v := range variants {
			if len(v.data) != 0 && !json.Valid(v.data) {
				t.Fatalf("%s must remain valid JSON to exercise the byte comparison", v.name)
			}
		}
	}
	passes, combinations := 0, 0
	for _, verify := range []bool{false, true} {
		for _, h := range hooks {
			for _, s := range settings {
				for _, c := range configs {
					combinations++
					t.Run(fmt.Sprintf("switch=%t/hooks=%s/settings=%s/mcp=%s", verify, h.name, s.name, c.name), func(t *testing.T) {
						want := !verify && h.name == "off" && s.name == "off" && c.name == "empty" || verify && h.name == "on" && s.name == "on" && c.name == "on"
						err := checkImplementerConfig(verify, h.data, s.data, c.data, "/trusted/bin/argonreview", "/trusted/source", "/trusted/worktree", "/trusted/guard")
						if (err == nil) != want {
							t.Fatalf("accepted=%t, want %t: %v", err == nil, want, err)
						}
						if err == nil {
							passes++
						}
						if err != nil && strings.Contains(err.Error(), "/trusted/") {
							t.Fatal("configuration error includes a path")
						}
					})
				}
			}
		}
	}
	t.Logf("checked %d combinations", combinations)
	if combinations != 2*len(hooks)*len(settings)*len(configs) {
		t.Fatal("incomplete configuration product")
	}
	if passes != 2 {
		t.Fatalf("accepted %d combinations, want exactly 2", passes)
	}
	if err := checkImplementerConfig(false, hooksOff, off, []byte{}, "/trusted/bin/argonreview", "", "", "/trusted/guard"); err != nil {
		t.Fatalf("empty and nil MCP bytes must agree: %v", err)
	}
}

func TestImplementVerifyUsage(t *testing.T) {
	_, dir := implementGuardFixture(t)
	valid := implementVerifyInput(`{"check":"build","package":"./..."}`)
	implementVerifyCheck(t, dir, valid, true)
	for _, args := range [][]string{
		{"implement-guard", dir, "--allow-verify", "extra"},
		{"implement-guard", dir, "other"}, {"implement-guard", "--allow-verify", dir},
		{"implement-guard", "--allow-verify"}, {"--allow-verify", "implement-guard", dir},
		{"implement-guard", "--allow-verify", dir, "--allow-verify"},
	} {
		var out, stderr bytes.Buffer
		if code := run(args, strings.NewReader(valid), &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "| implement-guard <dir> [--allow-verify]") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out.String(), stderr.String())
		}
	}
}
