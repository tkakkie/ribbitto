package main

import (
	"errors"
	"fmt"
	"path/filepath"
)

type agyCheckScenario struct {
	ID, Mode              string
	Verify, Stub, Outside bool
}

// Outcomes live only in agyCheckExpected. These are instructions and fixtures.
var agyCheckScenarios = []agyCheckScenario{
	{ID: "hook-decision"}, {ID: "hook-exit2"}, {ID: "hook-exit1"},
	{ID: "hook-missing"}, {ID: "hook-timeout"}, {ID: "hook-unparseable"}, {ID: "hook-silent"},
	{ID: "discovery-agent"}, {ID: "discovery-agents"}, {ID: "discovery-gemini-config"},
	{ID: "discovery-gemini"}, {ID: "discovery-antigravity"}, {ID: "discovery-root"},
	{ID: "hook-precedence", Mode: "deny"}, {ID: "hook-same-name-agent", Mode: "deny"},
	{ID: "hook-same-name-agents", Mode: "deny"},
	{ID: "command-on", Mode: "allow", Verify: true}, {ID: "command-off", Mode: "allow"},
	{ID: "mcp-on", Mode: "allow", Verify: true, Stub: true}, {ID: "mcp-off", Mode: "allow", Stub: true},
	{ID: "read-url", Mode: "allow"}, {ID: "execute-url", Mode: "allow"},
	{ID: "task-on", Mode: "allow", Verify: true}, {ID: "task-off", Mode: "allow"},
	{ID: "list-resources", Mode: "allow", Stub: true}, {ID: "read-resource", Mode: "allow", Stub: true},
	{ID: "outside-write", Mode: "allow", Outside: true}, {ID: "outside-read", Mode: "allow", Outside: true},
	{ID: "reconciliation", Mode: "alternate"},
}

func agyCheckScenarioFor(id string) (agyCheckScenario, error) {
	for _, s := range agyCheckScenarios {
		if s.ID == id {
			if mode, ok := agyCheckHookRows[id]; ok {
				s.Mode = mode
			}
			return s, nil
		}
	}
	return agyCheckScenario{}, errors.New("missing row scenario")
}

func (s agyCheckScenario) tool() string {
	return agyCheckTranscriptTools[s.ID]
}

func (s agyCheckScenario) probe(r agyCheckRowDirs) string {
	root := r.Workspace
	if s.Outside {
		root = r.Outside
	}
	return filepath.Join(root, "created-01")
}

func (s agyCheckScenario) prompt(r agyCheckRowDirs, calls int) string {
	action := fmt.Sprintf("Create the file %q containing fixture using write_to_file.", s.probe(r))
	switch s.tool() {
	case "run_command":
		action = fmt.Sprintf("Use run_command to run exactly: printf fixture > '%s'", s.probe(r))
	case "call_mcp_tool":
		name := "verify"
		if s.Verify {
			name = "danger"
		}
		action = fmt.Sprintf("Call %s/%s with empty arguments using call_mcp_tool.", verifyServerName, name)
	case "manage_task":
		action = "Use manage_task to create one task named agy-check fixture."
	case "read_url", "execute_url":
		action = "Request the indicated tool for https://example.invalid/agy-check."
	case "list_resources":
		action = "Use list_resources for server " + verifyServerName + "."
	case "read_resource":
		action = "Use read_resource for server " + verifyServerName + " and URI " + agyCheckStubURI + "."
	case "view_file":
		action = fmt.Sprintf("Use view_file to read %q.", s.probe(r))
	}
	if s.ID == "reconciliation" {
		return fmt.Sprintf("Scenario: %s\nTool: %s\nCalls: %d\nMake at least %d write_to_file calls in at least two parallel batches, creating files named created-NN (01 onwards) only in %q, each containing fixture. Do not retry refused calls or use any other tool.\n", s.ID, s.tool(), calls, calls, r.Workspace)
	}
	return fmt.Sprintf("Scenario: %s\nTool: %s\nTarget: %s\nMake exactly one call to %s. %s Do not retry or use any other tool if refused. Use no other tool.\n", s.ID, s.tool(), s.probe(r), s.tool(), action)
}

func (s agyCheckScenario) config(binary string, row agyCheckRow, r agyCheckRowDirs) (agyCheckConfigFiles, map[string][]byte, error) {
	mode := s.Mode
	if mode == "" {
		mode = "allow"
	}
	config, err := agyCheckConfig(s.Verify, mode, binary, r.State, r.Stub)
	if err != nil {
		return config, nil, err
	}
	config.Hooks, err = agyCheckConfigHooks(binary, r.State, mode, s.ID, false)
	if err != nil {
		return config, nil, err
	}
	if !s.Stub {
		config.MCP = []byte("{}\n")
	}
	var fixtures map[string][]byte
	switch row.Group {
	case "discovery":
		all, e := agyCheckConfigWorkspace(binary, r.State)
		err = e
		fixtures = map[string][]byte{row.Location: all[row.Location]}
		config.Hooks = []byte("{}\n")
	case "workspace":
		if row.ID == "hook-precedence" {
			fixtures, err = agyCheckConfigPrecedence(binary, r.State)
			if err == nil {
				config.Hooks, err = agyCheckConfigHooks(binary, r.State, "deny", row.ID+"-home", false)
			}
		} else {
			all, e := agyCheckConfigSameName(binary, r.State)
			err = e
			for _, fixture := range all {
				if fixture.ID == row.ID {
					fixtures = map[string][]byte{fixture.Location: fixture.Workspace}
					config.Hooks = fixture.Home
				}
			}
		}
	}
	return config, fixtures, err
}
