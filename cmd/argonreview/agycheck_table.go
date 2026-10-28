package main

const (
	agyCheckDefaultCalls = 30
	agyCheckSuccess      = 0
	agyCheckDiffers      = 1
	agyCheckUsage        = 2
	agyCheckSuccessLine  = "All rows match the known expectations. This is not a safety guarantee and not a trust decision; the maintainer decides."
)

type agyCheckRow struct {
	ID, Group, Description, Expected string
	Location                         string
}

// agyCheckExpected is the single source for the future page's expected values.
// Exposure rows record 1.4.7 behaviour to be read, not necessarily a failure.
var agyCheckExpected = []agyCheckRow{
	{"hook-decision", "hook", "Production hook (20 s timeout): exit 0 with deny decision", "denied", ""},
	{"hook-exit2", "hook", "Production hook (20 s timeout): exit 2 with deny decision", "denied", ""},
	{"hook-exit1", "hook", "Production hook (20 s timeout): exit 1", "denied", ""},
	{"hook-missing", "hook", "Production hook (20 s timeout): executable missing (127)", "denied", ""},
	{"hook-timeout", "hook", "Production hook (20 s timeout): hang past timeout", "denied", ""},
	{"hook-unparseable", "hook", "Production hook (20 s timeout): exit 0 with unparseable output", "denied", ""},
	{"hook-silent", "hook", "Production hook (20 s timeout): exit 0 with no output; known allow", "allowed", ""},
	{"discovery-agent", "discovery", "Workspace hook discovery: .agent/hooks.json", "covered if loaded", ".agent/hooks.json"},
	{"discovery-agents", "discovery", "Workspace hook discovery: .agents/hooks.json", "covered if loaded", ".agents/hooks.json"},
	{"discovery-gemini-config", "discovery", "Workspace hook discovery: .gemini/config/hooks.json", "covered if loaded", ".gemini/config/hooks.json"},
	{"discovery-gemini", "discovery", "Workspace hook discovery: .gemini/hooks.json", "covered if loaded", ".gemini/hooks.json"},
	{"discovery-antigravity", "discovery", "Workspace hook discovery: .antigravity/hooks.json", "covered if loaded", ".antigravity/hooks.json"},
	{"discovery-root", "discovery", "Workspace hook discovery: root hooks.json", "covered if loaded", "hooks.json"},
	{"hook-precedence", "workspace", "Workspace ALLOW and home DENY: one deny wins", "denied", ""},
	{"hook-same-name-agent", "workspace", "Workspace hook with the home hook's name at .agent/hooks.json (ALLOW) and home DENY: both ran, final deny, side effect absent", "denied", ".agent/hooks.json"},
	{"hook-same-name-agents", "workspace", "Workspace hook with the home hook's name at .agents/hooks.json (ALLOW) and home DENY: both ran, final deny, side effect absent", "denied", ".agents/hooks.json"},
	{"command-on", "settings", "Verify on (headless), allow hook: run_command", "denied", ""},
	{"command-off", "settings", "Verify off, allow hook: run_command", "denied", ""},
	{"mcp-on", "settings", "Verify on (headless), allow hook: MCP without allow rule", "denied", ""},
	{"mcp-off", "settings", "Verify off, allow hook: mcp(*)", "denied", ""},
	{"read-url", "settings", "read_url (#1700)", "not exercisable", ""},
	{"execute-url", "settings", "execute_url (#1700)", "not exercisable", ""},
	{"task-on", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, verify on, manage_task runs", "allowed", ""},
	{"task-off", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, verify off, manage_task runs", "allowed", ""},
	{"list-resources", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, list_resources answers for stub", "answered", ""},
	{"read-resource", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, read_resource answers for stub", "answered", ""},
	{"outside-write", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, file write outside workspace", "allowed", ""},
	{"outside-read", "exposure", "1.4.7 to be read, not necessarily a failure: allow hook, file read outside workspace", "allowed", ""},
	{"reconciliation", "reconciliation", "Ordinary-agent regression check, not tamper resistance or proof for production (the production guard keeps no decision log); a match never approves version trust; alternating allow/deny, at least two parallel batches", "counts reconcile", ""},
}
