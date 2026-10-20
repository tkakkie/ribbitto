package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// Fixed names prevent a caller from selecting another server or command. The
// guard and settings are two permission layers that must agree. This feature is
// off by default; these pure generators and checks do not enable it anywhere.
const verifyServerName = "ribbitto-verify"

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
	if len(fields) != 3 {
		return errors.New("MCP requires exactly ServerName, ToolName and Arguments")
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

func implementerMCPConfig(verify bool, binary, source, worktree string) ([]byte, error) {
	if !verify {
		return nil, nil
	}
	for _, path := range []string{binary, source, worktree} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("MCP paths must be absolute and clean")
		}
	}
	// Maps give agy's sorted keys, with its indentation and trailing newline.
	config := map[string]any{"mcpServers": map[string]any{verifyServerName: map[string]any{
		"args":    []string{"-i", "PATH=" + verifyMCPPath, binary, "verify-mcp", "--trusted-source", source, "--worktree", worktree},
		"command": "/usr/bin/env", "disabled": false,
	}}}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, errors.New("encoding MCP configuration")
	}
	return append(data, '\n'), nil
}

// checkImplementerConfig is for implement-check (#1434) and preflight (#1433)
// before and after each run. verify is their one trusted switch; guardVerify and
// the file bytes are observed state. The launchers must also reject changed
// hooks/settings/MCP files and denied_actions; those checks live outside here.
func checkImplementerConfig(verify, guardVerify bool, settings, mcpConfig []byte, binary, source, worktree string) error {
	if guardVerify != verify {
		return errors.New("implementer guard mode does not match verify switch")
	}
	if !bytes.Equal(settings, implementerSettings(verify)) {
		return errors.New("implementer settings do not match verify switch")
	}
	expected, err := implementerMCPConfig(verify, binary, source, worktree)
	if err != nil {
		return err
	}
	if !bytes.Equal(mcpConfig, expected) {
		return errors.New("implementer MCP configuration does not match verify switch")
	}
	return nil
}
