package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agyCheckConfigHomeGolden = `{
  "ribbitto-implementer-guard": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "command": "'/trusted tool/argonreview' agy-check hook deny '/check/hook'",
            "timeout": 20
          }
        ]
      }
    ]
  }
}
`
const agyCheckConfigWorkspaceGolden = `{
  "ribbitto-check-workspace": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "command": "'/trusted tool/argonreview' agy-check hook allow '/check/hook' discovery-agent",
            "timeout": 20
          }
        ]
      }
    ]
  }
}
`
const agyCheckConfigMCPGolden = `{
  "mcpServers": {
    "ribbitto-verify": {
      "args": [
        "-i",
        "PATH=/usr/bin:/bin",
        "/trusted tool/argonreview",
        "agy-check",
        "stub-mcp",
        "/check/stub"
      ],
      "command": "/usr/bin/env",
      "disabled": false
    }
  }
}
`

func TestAgyCheckConfigHooks(t *testing.T) {
	for _, tc := range []struct {
		mode, tag, golden string
		workspace         bool
	}{
		{"deny", "", agyCheckConfigHomeGolden, false},
		{"allow", "discovery-agent", agyCheckConfigWorkspaceGolden, true},
	} {
		data, err := agyCheckConfigHooks("/trusted tool/argonreview", "/check/hook", tc.mode, tc.tag, tc.workspace)
		if err != nil || string(data) != tc.golden {
			t.Fatalf("hooks golden drift: %s %v", data, err)
		}
	}
	for _, mode := range agyCheckHookModes {
		data, err := agyCheckConfigHooks("/binary", "/state", mode.name, "", false)
		if err != nil || !strings.Contains(string(data), "agy-check hook "+mode.name+" '/state'") {
			t.Fatalf("mode generation drift: %s %s %v", mode.name, data, err)
		}
	}
	production, err := implementerHooks(false, "/trusted tool/argonreview", "/check/hook")
	if err != nil {
		t.Fatal(err)
	}
	// Normalize only the command; event, matcher, name, timeout and formatting
	// must track the production generator independently of these goldens.
	home, err := agyCheckConfigHooks("/trusted tool/argonreview", "/check/hook", "deny", "", false)
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Replace(string(home), "agy-check hook deny", "implement-guard", 1)
	if normalized != string(production) {
		t.Fatalf("home hook differs from production structure: %s", production)
	}
}

func TestAgyCheckConfigMissing(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "argonreview")
	if err := os.WriteFile(binary, nil, 0o500); err != nil {
		t.Fatal(err)
	}
	data, err := agyCheckConfigHooks(binary, dir, agyCheckMissingHook, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]map[string][]struct {
		Hooks []struct{ Command string } `json:"hooks"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	command := config["ribbitto-implementer-guard"]["PreToolUse"][0].Hooks[0].Command
	missing := filepath.Join(dir, "hook-missing")
	if command != "'"+missing+"'" || missing == binary || !strings.HasPrefix(missing, dir+"/") {
		t.Fatalf("missing executable is not fixed under state: %q", command)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing executable unexpectedly exists: %v", err)
	}
	if err := os.WriteFile(missing, nil, 0o500); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{missing, strings.ToUpper(missing)} {
		if _, err := agyCheckConfigHooks(binary, dir, agyCheckMissingHook, "", false); err == nil {
			t.Fatalf("missing hook aliases an existing trusted binary: %q", binary)
		}
	}
	if _, ok := agyCheckHookFindMode(agyCheckMissingHook); ok {
		t.Fatal("missing executable became a hook mode")
	}
}

func TestAgyCheckConfigBundle(t *testing.T) {
	for _, verify := range []bool{false, true} {
		files, err := agyCheckConfig(verify, "deny", "/trusted tool/argonreview", "/check/hook", "/check/stub")
		if err != nil {
			t.Fatal(err)
		}
		if string(files.Hooks) != agyCheckConfigHomeGolden {
			t.Fatalf("home hooks differ from golden: %s", files.Hooks)
		}
		if !bytes.Equal(files.Settings, implementerSettings(verify)) {
			t.Fatalf("settings differ from production (verify=%t): %s", verify, files.Settings)
		}
		if string(files.MCP) != agyCheckConfigMCPGolden {
			t.Fatalf("MCP configuration differs from golden: %s", files.MCP)
		}
	}
}

func TestAgyCheckConfigRefusals(t *testing.T) {
	for _, path := range []string{"", "/", "relative", "/a/../b", "/a'b", "/a\nb", "/a\x00b", "/protected/account/settings.json", "/protected/review/SETTINGS.JSON/child"} {
		for index := range 3 {
			paths := []string{"/binary", "/hook", "/stub"}
			paths[index] = path
			if _, err := agyCheckConfig(false, "allow", paths[0], paths[1], paths[2]); err == nil {
				t.Fatalf("unsafe bundle path %d %q accepted", index, path)
			}
		}
		for index := range 2 {
			paths := []string{"/binary", "/state"}
			paths[index] = path
			if _, err := agyCheckConfigHooks(paths[0], paths[1], "allow", "", false); err == nil {
				t.Fatalf("unsafe hook path %q accepted", path)
			}
			if _, err := agyCheckConfigMCP(paths[0], paths[1]); err == nil {
				t.Fatalf("unsafe MCP path %q accepted", path)
			}
			if _, err := agyCheckConfigWorkspace(paths[0], paths[1]); err == nil {
				t.Fatalf("unsafe workspace path %q accepted", path)
			}
			if _, err := agyCheckConfigPrecedence(paths[0], paths[1]); err == nil {
				t.Fatalf("unsafe precedence path %q accepted", path)
			}
			if _, err := agyCheckConfigSameName(paths[0], paths[1]); err == nil {
				t.Fatalf("unsafe same-name path %q accepted", path)
			}
		}
	}
	for _, mode := range []string{"", "unknown", "allow extra"} {
		if _, err := agyCheckConfig(false, mode, "/binary", "/hook", "/stub"); err == nil {
			t.Fatalf("unknown mode %q accepted", mode)
		}
	}
	for _, tag := range []string{"-bad", "UPPER", "a b", "a'", "a\n", strings.Repeat("a", 41)} {
		if _, err := agyCheckConfigHooks("/binary", "/hook", "allow", tag, true); err == nil {
			t.Fatalf("invalid tag %q accepted", tag)
		}
	}
	for _, location := range []string{"", ".", "/absolute", "../outside", "a/../b", "a//b", "a/settings.json", "a'b", "a\nb"} {
		if agyCheckConfigLocation(location) {
			t.Fatalf("unsafe location %q accepted", location)
		}
	}
}

func TestAgyCheckConfigHookRows(t *testing.T) {
	seen := 0
	for _, row := range agyCheckExpected {
		mode, ok := agyCheckHookRows[row.ID]
		if ok != (row.Group == "hook") {
			t.Fatalf("hook row coverage drift: %s", row.ID)
		}
		if !ok {
			continue
		}
		seen++
		if _, err := agyCheckConfigHooks("/binary", "/state", mode, "", false); err != nil {
			t.Fatalf("invalid row mode: %s %v", row.ID, err)
		}
	}
	if seen != len(agyCheckHookRows) {
		t.Fatal("extra hook row mapping")
	}
	for id, mode := range map[string]string{"hook-decision": "deny", "hook-exit2": "exit2", "hook-exit1": "exit1", "hook-missing": "missing", "hook-timeout": "hang", "hook-unparseable": "unparseable", "hook-silent": "silent"} {
		if agyCheckHookRows[id] != mode {
			t.Fatalf("hook row %s mode: %q", id, agyCheckHookRows[id])
		}
	}
}

func agyCheckConfigAssertRefused(t *testing.T, location string) {
	t.Helper()
	for _, part := range strings.Split(location, "/") {
		for _, refused := range implementRefusedNames {
			if strings.EqualFold(part, refused) {
				return
			}
		}
	}
	t.Fatalf("expected covered hook path is not refused: %s", location)
}

func agyCheckConfigAssertWorkspace(t *testing.T) {
	t.Helper()
	fixtures, err := agyCheckConfigWorkspace("/binary", "/state")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range agyCheckExpected {
		if row.Group != "discovery" {
			continue
		}
		count++
		data, ok := fixtures[row.Location]
		want, err := agyCheckConfigHooks("/binary", "/state", "allow", row.ID, true)
		if err != nil || !ok || !bytes.Equal(data, want) {
			t.Fatalf("discovery fixture missing or wrong tag: %s %s %v", row.ID, data, err)
		}
		if row.Expected == "covered if loaded" {
			agyCheckConfigAssertRefused(t, row.Location)
		}
	}
	if len(fixtures) != count {
		t.Fatalf("discovery fixture set differs from table: %d, want %d", len(fixtures), count)
	}
}

func agyCheckConfigHomeCommand(t *testing.T, data []byte) string {
	t.Helper()
	var config map[string]map[string][]struct {
		Matcher string
		Hooks   []struct {
			Command string
			Timeout int
		}
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	events, ok := config["ribbitto-implementer-guard"]
	if len(config) != 1 || !ok || len(events) != 1 || len(events["PreToolUse"]) != 1 {
		t.Fatalf("same-name hook must use the home name and event: %s", data)
	}
	matcher := events["PreToolUse"][0]
	if matcher.Matcher != "*" || len(matcher.Hooks) != 1 || matcher.Hooks[0].Timeout != 20 {
		t.Fatalf("same-name matcher/hooks/timeout drift: %s", data)
	}
	return matcher.Hooks[0].Command
}

func agyCheckConfigAssertSameName(t *testing.T) {
	t.Helper()
	fixtures, err := agyCheckConfigSameName("/binary", "/state")
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	for _, row := range agyCheckExpected {
		if row.Group != "workspace" || row.Location == "" {
			continue
		}
		if index >= len(fixtures) {
			t.Fatalf("same-name fixture missing: %s", row.ID)
		}
		fixture := fixtures[index]
		index++
		if fixture.ID != row.ID || fixture.Location != row.Location {
			t.Fatalf("same-name row/location drift: %+v, want %s %s", fixture, row.ID, row.Location)
		}
		for _, hook := range []struct {
			data      []byte
			mode, tag string
		}{{fixture.Workspace, "allow", row.ID}, {fixture.Home, "deny", row.ID + "-home"}} {
			command := agyCheckConfigHomeCommand(t, hook.data)
			want := "'/binary' agy-check hook " + hook.mode + " '/state' " + hook.tag
			if command != want || !agyCheckHookTag(hook.tag) {
				t.Fatalf("same-name mode/tag drift: %q, want %q", command, want)
			}
		}
		agyCheckConfigAssertRefused(t, row.Location)
	}
	if len(fixtures) != index {
		t.Fatalf("extra same-name fixtures: %d, want %d", len(fixtures), index)
	}
}

func TestAgyCheckConfigSameName(t *testing.T) {
	agyCheckConfigAssertSameName(t)
	fixtures, err := agyCheckConfigSameName("/binary", "/state")
	if err != nil {
		t.Fatal(err)
	}
	wantLocations := map[string]string{"hook-same-name-agent": ".agent/hooks.json", "hook-same-name-agents": ".agents/hooks.json"}
	if len(fixtures) != len(wantLocations) {
		t.Fatalf("same-name fixture count: %d, want %d", len(fixtures), len(wantLocations))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			if fixture.Location != wantLocations[fixture.ID] {
				t.Fatalf("same-name location swapped: %s %s", fixture.ID, fixture.Location)
			}
		})
	}
	// Collision fixtures must not change the different-name scenarios.
	agyCheckConfigAssertWorkspace(t)
	discovery, err := agyCheckConfigWorkspace("/trusted tool/argonreview", "/check/hook")
	if err != nil || string(discovery[".agent/hooks.json"]) != agyCheckConfigWorkspaceGolden {
		t.Fatalf("discovery golden drift: %s %v", discovery[".agent/hooks.json"], err)
	}
	precedence, err := agyCheckConfigPrecedence("/trusted tool/argonreview", "/check/hook")
	want := strings.Replace(agyCheckConfigWorkspaceGolden, "discovery-agent", "hook-precedence", 1)
	if err != nil || len(precedence) != 1 || string(precedence[".agent/hooks.json"]) != want {
		t.Fatalf("precedence golden drift: %s %v", precedence[".agent/hooks.json"], err)
	}
}

func TestAgyCheckConfigSameNameTable(t *testing.T) {
	saved := agyCheckExpected
	defer func() { agyCheckExpected = saved }()
	agyCheckExpected = append(append([]agyCheckRow{}, saved...), agyCheckRow{
		ID: "hook-same-name-added", Group: "workspace", Expected: "denied", Location: ".agents/extra/hooks.json",
	})
	agyCheckConfigAssertSameName(t)
	for _, location := range []string{"../outside", "/absolute", "a/settings.json", "a'b", ".agent/hooks.json", ".agents/hooks.json"} {
		t.Run(location, func(t *testing.T) {
			agyCheckExpected[len(agyCheckExpected)-1].Location = location
			if _, err := agyCheckConfigSameName("/binary", "/state"); err == nil {
				t.Fatalf("unsafe/duplicate same-name location %q accepted", location)
			}
		})
	}
	agyCheckExpected[len(agyCheckExpected)-1].Location = ".agents/extra/hooks.json"
	for _, id := range []string{"", "-bad", "UPPER", strings.Repeat("a", 36)} {
		agyCheckExpected[len(agyCheckExpected)-1].ID = id
		if _, err := agyCheckConfigSameName("/binary", "/state"); err == nil {
			t.Fatalf("invalid workspace/home tag accepted: %q", id)
		}
	}
}

func TestAgyCheckConfigSameNameClaims(t *testing.T) {
	fixtures, err := agyCheckConfigSameName("/binary", "/state")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			dir := agyCheckHookPrepared(t)
			h := &agyCheckHookHarness{d: agyCheckHookFileDeps(strings.NewReader("{}"))}
			for i, data := range [][]byte{fixture.Home, fixture.Workspace} {
				command := agyCheckConfigHomeCommand(t, data)
				args, ok := strings.CutPrefix(command, "'/binary' agy-check hook ")
				mode, tag, found := strings.Cut(args, " '/state' ")
				if !ok || !found {
					t.Fatalf("unexpected generated command: %q", command)
				}
				output := "{\"decision\":\"allow\"}\n"
				if i == 0 {
					output = "{\"decision\":\"deny\",\"reason\":\"agy-check fixture denial\"}\n"
				}
				agyCheckHookRun(t, h, []string{mode, dir, tag}, 0, output, "")
			}
			calls := filepath.Join(dir, "calls")
			entries, err := os.ReadDir(calls)
			if err != nil || len(entries) != 2 {
				t.Fatalf("both hooks must leave claims: %d records %v", len(entries), err)
			}
			for i, want := range []string{"deny " + fixture.ID + "-home\n", "allow " + fixture.ID + "\n"} {
				data, err := os.ReadFile(filepath.Join(calls, entries[i].Name()))
				if err != nil || string(data) != want {
					t.Fatalf("claim %d: %q, want %q: %v", i+1, data, want, err)
				}
			}
		})
	}
}

func TestAgyCheckConfigWorkspace(t *testing.T) {
	agyCheckConfigAssertWorkspace(t)
	fixtures, err := agyCheckConfigPrecedence("/binary", "/state")
	want, wantErr := agyCheckConfigHooks("/binary", "/state", "allow", "hook-precedence", true)
	if err != nil || wantErr != nil || len(fixtures) != 1 || !bytes.Equal(fixtures[".agent/hooks.json"], want) {
		t.Fatalf("precedence fixture drift: %v %v", fixtures, err)
	}
	// Prove generation reads the table at call time rather than a copied list.
	saved := agyCheckExpected
	defer func() { agyCheckExpected = saved }()
	agyCheckExpected = append(append([]agyCheckRow{}, saved...), agyCheckRow{ID: "discovery-added", Group: "discovery", Expected: "covered if loaded", Location: ".agent/extra/hooks.json"})
	agyCheckConfigAssertWorkspace(t)
	for _, location := range []string{"../outside", "/absolute", "a/settings.json", ".agent/hooks.json"} {
		agyCheckExpected[len(agyCheckExpected)-1].Location = location
		if _, err := agyCheckConfigWorkspace("/binary", "/state"); err == nil {
			t.Fatalf("unsafe/duplicate discovery location %q accepted", location)
		}
	}
}
