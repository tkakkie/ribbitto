package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Fixed names prevent a caller from selecting another server or command. The
// guard and settings are two permission layers that must agree. This feature is
// off by default; these pure generators and checks do not enable it anywhere.
const verifyServerName = "ribbitto-verify"

const maxVerifyMetadataBytes = 1024

func implementVerifyCall(input, args []byte) error {
	// Check the whole input before decoding fields: even an unused nested object
	// must not hide duplicate keys, including escaped spellings of the same key.
	if _, err := verifyObject(input); err != nil {
		return fmt.Errorf("reading verify hook input: %w", err)
	}
	fields, err := verifyObject(args)
	if err != nil {
		return fmt.Errorf("reading MCP arguments: %w", err)
	}
	for key, raw := range fields {
		switch key {
		case "ServerName", "ToolName", "Arguments":
		case "toolAction", "toolSummary":
			var value string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil || len(value) > maxVerifyMetadataBytes {
				return errors.New("MCP metadata must be a bounded string")
			}
		default:
			return errors.New("MCP argument key is not allowed")
		}
	}
	for _, key := range []string{"ServerName", "ToolName", "Arguments"} {
		if _, ok := fields[key]; !ok {
			return errors.New("MCP requires ServerName, ToolName and Arguments")
		}
	}
	var server, tool string
	if json.Unmarshal(fields["ServerName"], &server) != nil || server != verifyServerName ||
		json.Unmarshal(fields["ToolName"], &tool) != nil || tool != verifyToolName {
		return errors.New("MCP server or tool does not match")
	}
	// Share the server's strict parser, not a second interpretation of its schema.
	if _, err := parseVerifyArguments(fields["Arguments"]); err != nil {
		return fmt.Errorf("validating MCP Arguments: %w", err)
	}
	return nil
}

// implementerHooks generates the Go command form of the implementer's hooks.json.
// The goldens use placeholder paths and the same template; with the production
// paths the off golden is byte-identical to the production file.
func implementerHooks(verify bool, binary, dir string) ([]byte, error) {
	// sh -c interprets this command, so quote both paths and refuse text that
	// could break out of the quotes or produce a different command line.
	for _, path := range []string{binary, dir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "'\n") {
			return nil, errors.New("hook paths must be absolute, clean and safely quotable")
		}
	}
	command := "'" + binary + "' implement-guard '" + dir + "'"
	if verify {
		command += " --allow-verify"
	}
	type hook struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	data, err := json.MarshalIndent(map[string]map[string][]matcher{
		"ribbitto-implementer-guard": {"PreToolUse": {{"*", []hook{{command, 20}}}}},
	}, "", "  ")
	if err != nil {
		return nil, errors.New("encoding implementer hooks")
	}
	return append(data, '\n'), nil
}

func implementerSettings(verify bool) []byte {
	allow := []string{"write_file(*)"}
	deny := []string{"command(*)", "read_url(*)", "execute_url(*)"}
	if verify {
		allow = append(allow, "mcp("+verifyServerName+"/"+verifyToolName+")")
	} else {
		deny = append(deny, "mcp(*)")
	}
	settings := struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}{}
	settings.Permissions.Allow, settings.Permissions.Deny = allow, deny
	// Only strings and slices are marshaled, so encoding cannot fail.
	data, _ := json.MarshalIndent(settings, "", "  ")
	return append(data, '\n')
}

func implementerMCPConfig(verify bool, binary, source, worktree, browserMode string) ([]byte, error) {
	if browserMode != "" {
		if !verify {
			return nil, errors.New("browser-mode requires verify on")
		}
		if err := browserModePath(browserMode); err != nil {
			return nil, err
		}
	}
	if !verify {
		return nil, nil
	}
	for _, path := range []string{binary, source, worktree} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("MCP paths must be absolute and clean")
		}
	}
	args := []string{"-i", "PATH=" + verifyMCPPath, binary, "verify-mcp", "--trusted-source", source, "--worktree", worktree}
	if browserMode != "" {
		args = append(args, "--browser-mode", browserMode)
	}
	// Maps give agy's sorted keys, with its indentation and trailing newline.
	config := map[string]any{"mcpServers": map[string]any{verifyServerName: map[string]any{
		"args":    args,
		"command": "/usr/bin/env", "disabled": false,
	}}}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, errors.New("encoding MCP configuration")
	}
	return append(data, '\n'), nil
}

// checkImplementerConfig is for implement-check (#1434) and preflight (#1433)
// before and after each run. verify is their one trusted switch; hooks.json is
// the one place the guard mode is read from. All file bytes are observed state.
// The launchers must also reject changed hooks/settings/MCP files and
// denied_actions; those checks live outside here.
func checkImplementerConfig(verify bool, hooks, settings, mcpConfig []byte, binary, source, worktree, guardDir, browserMode string) error {
	expectedHooks, err := implementerHooks(verify, binary, guardDir)
	if err != nil {
		return err
	}
	if !bytes.Equal(hooks, expectedHooks) {
		return errors.New("implementer hooks do not match verify switch")
	}
	if !bytes.Equal(settings, implementerSettings(verify)) {
		return errors.New("implementer settings do not match verify switch")
	}
	expected, err := implementerMCPConfig(verify, binary, source, worktree, browserMode)
	if err != nil {
		return err
	}
	if !bytes.Equal(mcpConfig, expected) {
		return errors.New("implementer MCP configuration does not match verify switch")
	}
	return nil
}
