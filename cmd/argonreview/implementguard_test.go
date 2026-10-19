package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func implementGuardTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func implementGuardWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func implementGuardFixture(t *testing.T) (string, string) {
	t.Helper()
	root, dir := implementGuardTempDir(t), implementGuardTempDir(t)
	for _, name := range []string{"sub", ".github", ".git"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.go", "sub/b.go", "AGENTS.md", ".github/w.yml"} {
		implementGuardWrite(t, filepath.Join(root, name), "x")
	}
	implementGuardWrite(t, filepath.Join(dir, "implementer-worktree"), root)
	return root, dir
}

func implementGuardInput(t *testing.T, tool string, args any) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"toolCall": map[string]any{"name": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

func implementGuardResult(t *testing.T, code int, out, stderr, want string) {
	t.Helper()
	decision := "deny"
	wantCode := 2
	wantStderr := "implementer guard: " + want + "\n"
	if want == "" {
		decision, wantCode, wantStderr = "allow", 0, ""
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decision %q: %v", out, err)
	}
	fields := 2
	if want == "" {
		fields = 1
	}
	if code != wantCode || got["decision"] != decision || got["reason"] != want || len(got) != fields || stderr != wantStderr || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d, %s, reason %q", code, out, stderr, wantCode, decision, want)
	}
}

func implementGuardCheck(t *testing.T, dir, input, want string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := run([]string{"implement-guard", dir}, strings.NewReader(input), &out, &stderr)
	implementGuardResult(t, code, out.String(), stderr.String(), want)
}

func TestImplementGuardRecorded(t *testing.T) {
	data, err := os.ReadFile("testdata/implement-guard/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Case, Tool, Decision, Reason string
		Args                         json.RawMessage
		Exit                         int
		Symlink                      bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 33 {
		t.Fatalf("recorded %d cases, want 33", len(cases))
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d_%s", i+1, tc.Case), func(t *testing.T) {
			root, dir := implementGuardFixture(t)
			if tc.Symlink {
				if err := os.Symlink("a.go", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			// Marshal the substituted path as JSON too, so even a temporary
			// directory containing quotes or backslashes remains valid input.
			quoted, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			args := json.RawMessage(strings.ReplaceAll(string(tc.Args), "<W>", string(quoted[1:len(quoted)-1])))
			input := implementGuardInput(t, tc.Tool, args)
			var out, stderr bytes.Buffer
			code := run([]string{"implement-guard", dir}, strings.NewReader(input), &out, &stderr)
			want := strings.ReplaceAll(tc.Reason, "<W>", root)
			implementGuardResult(t, code, out.String(), stderr.String(), want)
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

func TestImplementGuardToolsAndProtectedNames(t *testing.T) {
	_, dir := implementGuardFixture(t)
	tools := []string{"view_file", "list_dir", "grep_search", "find_by_name", "write_to_file", "replace_file_content", "multi_replace_file_content"}
	// Independent of the implementation table, so removing a protected name
	// cannot silently remove its tests as well.
	protected := []string{
		".github", ".git", ".gitattributes", ".gitmodules", ".agents", ".agent",
		"_agents", "_agent", ".gemini", ".claude", ".codex", ".mcp.json",
		"hooks.json", "plugin.json", "skills.json", "plugins.json", "gemini.md",
		"agents.md", "agents.override.md", "claude.md", "claude.local.md",
	}
	if strings.Join(protected, "\n") != strings.Join(implementRefusedNames, "\n") {
		t.Fatal("refused-name table changed")
	}
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			implementGuardCheck(t, dir, implementGuardInput(t, tool, map[string]string{"path": "."}), "")
			for _, name := range protected {
				for _, part := range []string{name, strings.ToUpper(name)} {
					for _, prefix := range []string{"", "new/nested/"} {
						path := prefix + part + "/new"
						t.Run(path, func(t *testing.T) {
							implementGuardCheck(t, dir, implementGuardInput(t, tool, map[string]string{"path": path}), path+" touches a refused name ("+part+")")
						})
					}
				}
			}
		})
	}
	for _, tool := range []string{"call_mcp_tool", "manage_task", "list_resources", "read_resource", "run_command", "search_web", "", "unknown"} {
		t.Run("denied_"+tool, func(t *testing.T) {
			implementGuardCheck(t, dir, implementGuardInput(t, tool, map[string]string{"path": "a.go"}), tool+" is not allowed in the implementer trial")
		})
	}
}

func TestImplementGuardInputs(t *testing.T) {
	root, dir := implementGuardFixture(t)
	noPaths := "view_file names no path the guard can check"
	for _, tc := range []struct{ name, input, reason string }{
		{"malformed", `{"toolCall":`, "guard error: unreadable hook input"},
		{"empty", "", "guard error: unreadable hook input"},
		{"missing toolCall", `{}`, "guard error: missing or invalid toolCall"},
		{"null toolCall", `{"toolCall":null}`, "guard error: missing or invalid toolCall"},
		{"case sensitive toolCall", `{"ToolCall":{"name":"view_file","args":{"path":"a.go"}}}`, "guard error: missing or invalid toolCall"},
		{"case sensitive name", `{"toolCall":{"Name":"view_file","args":{"path":"a.go"}}}`, "tool name is not a string"},
		{"missing name", `{"toolCall":{}}`, "tool name is not a string"},
		{"numeric name", `{"toolCall":{"name":7}}`, "tool name is not a string"},
		{"null name", `{"toolCall":{"name":null}}`, "tool name is not a string"},
		{"object name", `{"toolCall":{"name":{}}}`, "tool name is not a string"},
		{"list name", `{"toolCall":{"name":[]}}`, "tool name is not a string"},
		{"bool name", `{"toolCall":{"name":false}}`, "tool name is not a string"},
		{"oversized", strings.Repeat(" ", maxGuardInput+1), "hook input too large"},
		{"oversized UTF8 bytes", `{"unused":"` + strings.Repeat("é", maxGuardInput/2) + `","toolCall":{"name":"view_file","args":{"path":"a.go"}}}`, "hook input too large"},
		{"valid truncated prefix", `{"toolCall":{"name":"view_file","args":{"path":"a.go"}}}` + strings.Repeat(" ", maxGuardInput), "hook input too large"},
		{"trailing JSON", `{"toolCall":{"name":"view_file","args":{"path":"a.go"}}}{}`, "guard error: unreadable hook input"},
		{"nonstandard number", `{"toolCall":{"name":"view_file","args":{"path":NaN,"file":"a.go"}}}`, "guard error: unreadable hook input"},
		{"infinity", `{"toolCall":{"name":"view_file","args":{"path":Infinity,"file":"a.go"}}}`, "guard error: unreadable hook input"},
		{"negative infinity", `{"toolCall":{"name":"view_file","args":{"path":-Infinity,"file":"a.go"}}}`, "guard error: unreadable hook input"},
		{"invalid UTF8", "{\"unused\":\"\xff\"}", "guard error: invalid Unicode input"},
		{"unpaired high surrogate", `{"toolCall":{"name":"view_file","args":{"path":"a.go"}},"unused":"\ud800"}`, "guard error: invalid Unicode input"},
		{"unpaired low surrogate", `{"toolCall":{"name":"view_file","args":{"path":"\udc00"}}}`, "guard error: invalid Unicode input"},
		{"missing args", `{"toolCall":{"name":"view_file"}}`, noPaths},
		{"integer over Python limit", `{"unused":` + strings.Repeat("1", 4301) + `,"toolCall":{"name":"view_file","args":{"path":"a.go"}}}`, "guard error: JSON exceeds safe parsing limits"},
		{"integer at Python limit", `{"unused":` + strings.Repeat("1", 4300) + `,"toolCall":{"name":"view_file","args":{"path":"a.go"}}}`, ""},
		{"deep unused object", `{"unused":` + strings.Repeat("[", 512) + "0" + strings.Repeat("]", 512) + `,"toolCall":{"name":"view_file","args":{"path":"a.go"}}}`, "guard error: JSON exceeds safe parsing limits"},
		{"bounded unused object", `{"unused":` + strings.Repeat("[", 511) + "0" + strings.Repeat("]", 511) + `,"toolCall":{"name":"view_file","args":{"path":"a.go"}}}`, ""},
		{"key order", `{"toolCall":{"name":"view_file","args":{"zpath":"AGENTS.md","apath":"../x"}}}`, "AGENTS.md touches a refused name (AGENTS.md)"},
		{"reverse key order", `{"toolCall":{"name":"view_file","args":{"apath":"../x","zpath":"AGENTS.md"}}}`, "../x leaves the worktree"},
		{"duplicate key last value", `{"toolCall":{"name":"view_file","args":{"path":"../x","path":"a.go"}}}`, ""},
		{"duplicate key first position", `{"toolCall":{"name":"view_file","args":{"zpath":"a.go","apath":"../x","zpath":"AGENTS.md"}}}`, "AGENTS.md touches a refused name (AGENTS.md)"},
	} {
		t.Run(tc.name, func(t *testing.T) { implementGuardCheck(t, dir, tc.input, tc.reason) })
	}
	for _, tc := range []struct{ args, reason string }{
		{`null`, noPaths}, {`false`, noPaths}, {`0`, noPaths}, {`-0.0`, noPaths}, {`""`, noPaths}, {`[ ]`, noPaths}, {`{ }`, noPaths},
		{`true`, "guard error: args is not an object"}, {`1`, "guard error: args is not an object"}, {`"x"`, "guard error: args is not an object"}, {`["a.go"]`, "guard error: args is not an object"},
		{`{"path":false}`, noPaths}, {`{"path":42}`, noPaths}, {`{"path":1.25}`, noPaths}, {`{"path":1e999}`, noPaths}, {`{"path":[]}`, noPaths},
		{`{"path":[null]}`, "path is not a string"}, {`{"path":[false]}`, "path is not a string"}, {`{"path":[1]}`, "path is not a string"},
		{`{"path":[[]]}`, "path is not a string"}, {`{"path":[{}]}`, "path is not a string"},
		{`{"path":null,"file":true,"dir":9,"URI":"a.go"}`, ""},
		{`{"profile":"a.go"}`, ""}, {`{"FILE":"sub/../a.go"}`, ""}, {`{"path":""}`, ""},
		{`{"path":".git/../a.go"}`, ""}, {`{"path":"file://../x"}`, "../x leaves the worktree"},
		{`{"path":"file:///etc/hosts"}`, "/etc/hosts leaves the worktree"},
		{`{"path":"sub/../../x"}`, "sub/../../x leaves the worktree"},
		{`{"path":"a.go/child"}`, "guard error: checking requested path: lstat " + filepath.Join(root, "a.go", "child") + ": not a directory"},
		{`{"path":"AGENTS.md","dir":{}}`, "dir is not a string"},
		{`{"fİle":"a.go"}`, noPaths}, {`{"path":"sKills.json"}`, "sKills.json touches a refused name (sKills.json)"},
		{`{"path":"skİlls.json"}`, "skİlls.json touches a refused name (skİlls.json)"},
		{`{"path":"\ud83d\ude00.go"}`, ""}, {`{"path":"\\ud800"}`, ""}, {`{"path":"\ufffd.go"}`, ""},
	} {
		t.Run(tc.args, func(t *testing.T) {
			implementGuardCheck(t, dir, implementGuardInput(t, "view_file", json.RawMessage(tc.args)), tc.reason)
		})
	}
	for _, path := range []string{"/" + root + "/a.go", "file:///" + root + "/a.go"} {
		t.Run("double slash "+path, func(t *testing.T) {
			implementGuardCheck(t, dir, implementGuardInput(t, "view_file", map[string]string{"path": path}), strings.TrimPrefix(path, "file://")+" leaves the worktree")
		})
	}
	// Python's root + separator check also refuses children when root is /.
	for _, path := range []string{"a.go", "/a.go"} {
		if got := implementGuardPath("/", path, os.Lstat); got != path+" leaves the worktree" {
			t.Errorf("root /, path %q: %q", path, got)
		}
	}
}

func TestImplementGuardWorktree(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"missing path file", "guard error: reading worktree:"},
		{"relative", "worktree path is not a clean real path"},
		{"symlinked", "worktree path is not a clean real path"},
		{"absent", "worktree path is not a clean real path"},
		{"regular file", "guard error: worktree is not a directory"},
		{"not clean", "worktree path is not a clean real path"},
		{"trailing slash", "worktree path is not a clean real path"},
		{"whitespace", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := implementGuardFixture(t)
			pathFile := filepath.Join(dir, "implementer-worktree")
			path := root
			switch tc.name {
			case "missing path file":
				if err := os.Remove(pathFile); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "relative"
			case "symlinked":
				path = filepath.Join(dir, "link")
				if err := os.Symlink(root, path); err != nil {
					t.Fatal(err)
				}
			case "absent":
				path = filepath.Join(root, "absent")
			case "regular file":
				path = filepath.Join(root, "a.go")
			case "not clean":
				path += "/sub/.."
			case "trailing slash":
				path += "/"
			case "whitespace":
				path = " \t\n\x1c" + path + "\x1f\r\n"
			}
			if tc.name != "missing path file" {
				implementGuardWrite(t, pathFile, path)
			}
			var out, stderr bytes.Buffer
			code := implementGuard(dir, strings.NewReader(implementGuardInput(t, "view_file", map[string]string{"path": "a.go"})), &out, &stderr)
			var decision map[string]string
			if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(decision["reason"], tc.reason) || tc.reason == "" && decision["reason"] != "" {
				t.Fatalf("reason %q, want prefix %q", decision["reason"], tc.reason)
			}
			implementGuardResult(t, code, out.String(), stderr.String(), decision["reason"])
		})
	}
}

func TestImplementGuardSymlinks(t *testing.T) {
	for _, target := range []string{"a.go", "sub", "absent"} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_nested_%t", target, nested), func(t *testing.T) {
				root, dir := implementGuardFixture(t)
				link := filepath.Join(root, "link")
				if nested {
					link = filepath.Join(root, "sub", "link")
				}
				if err := os.Symlink(filepath.Join(root, target), link); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"a.go", "AGENTS.md", "../outside"} {
					implementGuardCheck(t, dir, implementGuardInput(t, "view_file", map[string]string{"path": path}), "the worktree holds a symlink")
				}
				implementGuardCheck(t, dir, implementGuardInput(t, "run_command", nil), "run_command is not allowed in the implementer trial")
				implementGuardCheck(t, dir, implementGuardInput(t, "view_file", nil), "view_file names no path the guard can check")
			})
		}
	}
}

func TestImplementGuardFilesystemErrors(t *testing.T) {
	for _, mode := range []string{"unlistable directory", "vanished entry", "walk return error", "empty walk error", "scan lstat error", "scan lstat vanished", "root lstat error", "prefix lstat error", "prefix symlink"} {
		t.Run(mode, func(t *testing.T) {
			root, dir := implementGuardFixture(t)
			walk := filepath.WalkDir
			lstat := os.Lstat
			path, want := "a.go", ""
			switch mode {
			case "unlistable directory", "vanished entry":
				walk = func(root string, fn fs.WalkDirFunc) error {
					return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
						if path == filepath.Join(root, "sub") {
							if mode == "vanished entry" {
								return fn(filepath.Join(path, "vanished"), nil, fs.ErrNotExist)
							}
							return fn(path, entry, fs.ErrPermission)
						}
						return fn(path, entry, err)
					})
				}
				want = "guard error: scanning worktree: walking worktree: " + fs.ErrPermission.Error()
				if mode == "vanished entry" {
					want = "guard error: scanning worktree: walking worktree: " + fs.ErrNotExist.Error()
				}
			case "walk return error":
				walk = func(string, fs.WalkDirFunc) error { return errors.New("walk failed") }
				want = "guard error: scanning worktree: walk failed"
			case "empty walk error":
				walk = func(string, fs.WalkDirFunc) error { return errors.New("") }
				want = "guard error: scanning worktree: "
			case "scan lstat error", "scan lstat vanished", "root lstat error", "prefix lstat error":
				bad := filepath.Join(root, "a.go")
				failure := fs.ErrPermission
				if mode == "scan lstat vanished" {
					failure = fs.ErrNotExist
				}
				want = "guard error: scanning worktree: checking worktree entry: " + failure.Error()
				if mode == "root lstat error" {
					bad, want = root, "guard error: checking worktree: "+fs.ErrPermission.Error()
				}
				if mode == "prefix lstat error" {
					path = "new/child"
					bad, want = filepath.Join(root, "new"), "guard error: checking requested path: "+fs.ErrPermission.Error()
				}
				lstat = func(path string) (fs.FileInfo, error) {
					if path == bad {
						return nil, failure
					}
					return os.Lstat(path)
				}
			case "prefix symlink":
				// Simulate a symlink appearing after the scan, without races.
				walk = func(root string, fn fs.WalkDirFunc) error {
					if err := filepath.WalkDir(root, fn); err != nil {
						return err
					}
					return os.Symlink("sub", filepath.Join(root, "new"))
				}
				path, want = "new/child", "new/child goes through a symlink"
			}
			var out, stderr bytes.Buffer
			code := implementGuardWithFS(dir, strings.NewReader(implementGuardInput(t, "view_file", map[string]string{"path": path})), &out, &stderr, walk, lstat)
			implementGuardResult(t, code, out.String(), stderr.String(), want)
		})
	}
}

// A short write without an error still loses the decision, despite violating
// io.Writer's contract; accepting it would turn a broken hook into permission.
type implementGuardShortWriter struct{}

func (implementGuardShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestImplementGuardIOFailures(t *testing.T) {
	_, dir := implementGuardFixture(t)
	allow := implementGuardInput(t, "view_file", map[string]string{"path": "a.go"})
	deny := implementGuardInput(t, "run_command", nil)
	for _, tc := range []struct {
		name           string
		stdin          io.Reader
		stdout, stderr io.Writer
	}{
		{"stdin", failingReader{}, &bytes.Buffer{}, &bytes.Buffer{}},
		{"stdin after bytes", &allowThenFail{}, &bytes.Buffer{}, &bytes.Buffer{}},
		{"allow stdout", strings.NewReader(allow), brokenWriter{0}, &bytes.Buffer{}},
		{"partial allow stdout", strings.NewReader(allow), brokenWriter{5}, &bytes.Buffer{}},
		{"short stdout", strings.NewReader(allow), implementGuardShortWriter{}, &bytes.Buffer{}},
		{"deny stdout", strings.NewReader(deny), brokenWriter{0}, &bytes.Buffer{}},
		{"partial deny stdout", strings.NewReader(deny), brokenWriter{5}, &bytes.Buffer{}},
		{"deny stderr", strings.NewReader(deny), &bytes.Buffer{}, brokenWriter{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := implementGuard(dir, tc.stdin, tc.stdout, tc.stderr); code == 0 {
				t.Fatal("undelivered decision or input failure exited 0")
			}
		})
	}
	var out bytes.Buffer
	if code := implementGuard(dir, strings.NewReader(allow), &out, brokenWriter{0}); code != 0 || out.String() != "{\"decision\":\"allow\"}\n" {
		t.Fatalf("delivered allow: exit %d, stdout %q", code, out.String())
	}
}

func TestImplementGuardUsage(t *testing.T) {
	for _, args := range [][]string{{"implement-guard"}, {"implement-guard", "dir", "extra"}} {
		var out, stderr bytes.Buffer
		if code := run(args, nil, &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "| implement-guard <dir>") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out.String(), stderr.String())
		}
	}
}
