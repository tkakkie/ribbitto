package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Missing is a configuration form, never an invocable hook mode.
const agyCheckMissingHook = "missing"

var agyCheckHookRows = map[string]string{
	"hook-decision": "deny", "hook-exit2": "exit2", "hook-exit1": "exit1",
	"hook-missing": agyCheckMissingHook, "hook-timeout": "hang",
	"hook-unparseable": "unparseable", "hook-silent": "silent",
}

type agyCheckConfigFiles struct{ Hooks, Settings, MCP []byte }

func agyCheckConfigPaths(paths ...string) error {
	for _, path := range paths {
		if !agyCheckCleanPath(path) || agyCheckSettingsPath(path) {
			return errors.New("configuration paths must be absolute, clean, non-root, safely quotable and not settings paths")
		}
	}
	return nil
}

func agyCheckConfigJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding agy-check configuration: %w", err)
	}
	return append(data, '\n'), nil
}

func agyCheckConfigHooks(binary, dir, mode, tag string, workspace bool) ([]byte, error) {
	if err := agyCheckConfigPaths(binary, dir); err != nil {
		return nil, err
	}
	if _, ok := agyCheckHookFindMode(mode); !ok && mode != agyCheckMissingHook {
		return nil, errors.New("unknown hook mode")
	}
	if tag != "" && !agyCheckHookTag(tag) {
		return nil, errors.New("invalid hook tag")
	}
	command := "'" + binary + "' agy-check hook " + mode + " '" + dir + "'"
	if tag != "" {
		command += " " + tag
	}
	if mode == agyCheckMissingHook {
		missing := filepath.Join(dir, "hook-missing")
		// A caller must not turn the missing fixture into the trusted binary.
		if strings.EqualFold(missing, binary) {
			return nil, errors.New("missing hook path must differ from the binary")
		}
		command = "'" + missing + "'"
	}
	name := "ribbitto-implementer-guard"
	if workspace {
		name = "ribbitto-check-workspace"
	}
	type hook struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	return agyCheckConfigJSON(map[string]map[string][]matcher{
		name: {"PreToolUse": {{"*", []hook{{command, agyCheckHookTimeout}}}}},
	})
}

func agyCheckConfigLocation(location string) bool {
	return location != "" && location != "." && !filepath.IsAbs(location) && filepath.Clean(location) == location &&
		!strings.Contains(location, "..") && !agyCheckSettingsPath(location) && agyCheckCleanPath("/"+location)
}

func agyCheckConfigWorkspace(binary, dir string) (map[string][]byte, error) {
	if err := agyCheckConfigPaths(binary, dir); err != nil {
		return nil, err
	}
	fixtures := make(map[string][]byte)
	for _, row := range agyCheckExpected {
		location := row.Location
		if row.Group != "discovery" {
			continue
		}
		if !agyCheckConfigLocation(location) {
			return nil, errors.New("workspace hook location must be relative, clean and not a settings path")
		}
		data, err := agyCheckConfigHooks(binary, dir, "allow", row.ID, true)
		if err != nil {
			return nil, err
		}
		if _, exists := fixtures[location]; exists {
			return nil, errors.New("duplicate workspace hook location")
		}
		fixtures[location] = data
	}
	return fixtures, nil
}

// Precedence uses the same location as discovery-agent, but in a separate row.
func agyCheckConfigPrecedence(binary, dir string) (map[string][]byte, error) {
	data, err := agyCheckConfigHooks(binary, dir, "allow", "hook-precedence", true)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{".agent/hooks.json": data}, nil
}

type agyCheckSameNameConfig struct {
	ID, Location    string
	Workspace, Home []byte
}

// Each collision is a separate scenario, with distinct claims for both hooks.
func agyCheckConfigSameName(binary, dir string) ([]agyCheckSameNameConfig, error) {
	if err := agyCheckConfigPaths(binary, dir); err != nil {
		return nil, err
	}
	var fixtures []agyCheckSameNameConfig
	seen := make(map[string]bool)
	for _, row := range agyCheckExpected {
		if row.Group != "workspace" || row.Location == "" {
			continue
		}
		if !agyCheckConfigLocation(row.Location) {
			return nil, errors.New("same-name hook location must be relative, clean and not a settings path")
		}
		if seen[row.Location] {
			return nil, errors.New("duplicate same-name hook location")
		}
		seen[row.Location] = true
		workspace, err := agyCheckConfigHooks(binary, dir, "allow", row.ID, false)
		if err != nil {
			return nil, err
		}
		home, err := agyCheckConfigHooks(binary, dir, "deny", row.ID+"-home", false)
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, agyCheckSameNameConfig{row.ID, row.Location, workspace, home})
	}
	return fixtures, nil
}

func agyCheckConfigMCP(binary, dir string) ([]byte, error) {
	if err := agyCheckConfigPaths(binary, dir); err != nil {
		return nil, err
	}
	// B3 supplies the stub-mcp server (verify and danger tools, plus resources).
	// B1 generates its configuration only; the subcommand is not registered.
	return agyCheckConfigJSON(map[string]any{"mcpServers": map[string]any{verifyServerName: map[string]any{
		"args":    []string{"-i", "PATH=" + verifyMCPPath, binary, "agy-check", "stub-mcp", dir},
		"command": "/usr/bin/env", "disabled": false,
	}}})
}

// B3 places these bytes; generation never examines or modifies any home.
func agyCheckConfig(verify bool, mode, binary, hookDir, stubDir string) (agyCheckConfigFiles, error) {
	var files agyCheckConfigFiles
	if err := agyCheckConfigPaths(binary, hookDir, stubDir); err != nil {
		return files, err
	}
	var err error
	files.Hooks, err = agyCheckConfigHooks(binary, hookDir, mode, "", false)
	if err != nil {
		return files, err
	}
	files.Settings = implementerSettings(verify)
	files.MCP, err = agyCheckConfigMCP(binary, stubDir)
	return files, err
}
