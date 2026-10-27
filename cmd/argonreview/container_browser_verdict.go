package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// browserLeafLimit bounds how much one named test may run inside the 10-minute
// container limit, so a group name cannot stand in for the whole suite.
const browserLeafLimit = 64

// stdout is not authenticated: test code can print these events itself. This
// is Argon's own check, never a security control or a substitute for CI.
func browserVerdict(importPath, run string, code int, runErr error, data []byte, truncated bool) string {
	if validateBrowserRun(run) != nil {
		return "FAILED: invalid browser run"
	}
	if verdict := testVerdictWithAllowlist(code, runErr, data, truncated, nil); !strings.HasPrefix(verdict, "verified") {
		return verdict
	}
	tests := map[string]string{}
	outside := false
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var e containerTestEvent
		if distinctKeys(line) != nil || json.Unmarshal(line, &e) != nil || e.Package == "" {
			return "FAILED: malformed test stream"
		}
		switch e.Action {
		case "start", "output", "pause", "cont":
		case "run", "pass", "fail", "skip":
			if e.Test == "" {
				if e.Action == "run" {
					return "FAILED: malformed test stream"
				}
				continue
			}
			if e.Package != importPath {
				outside = true
				continue
			}
			state, exists := tests[e.Test]
			if e.Action == "run" {
				if exists {
					return "FAILED: malformed test stream"
				}
				tests[e.Test] = ""
			} else {
				if !exists || state != "" {
					return "FAILED: malformed test stream"
				}
				tests[e.Test] = e.Action
			}
		default:
			return "FAILED: malformed test stream"
		}
	}
	return browserScope(run, tests, outside)
}

func browserScope(run string, tests map[string]string, outside bool) string {
	if tests[run] != "pass" {
		return "NOT VERIFIED: the named test did not run: " + run
	}
	parents := map[string]bool{}
	for name, state := range tests {
		if name != run && !strings.HasPrefix(name, run+"/") && !strings.HasPrefix(run, name+"/") {
			outside = true
		}
		if state != "pass" {
			return "FAILED: malformed test stream"
		}
		for parent := name; ; {
			i := strings.LastIndexByte(parent, '/')
			if i < 0 {
				break
			}
			parent = parent[:i]
			parents[parent] = true
		}
	}
	if outside {
		return "NOT VERIFIED: a test outside the named test ran"
	}
	leaves := 0
	for name := range tests {
		if (name == run || strings.HasPrefix(name, run+"/")) && !parents[name] {
			leaves++
		}
	}
	if leaves > browserLeafLimit {
		return fmt.Sprintf("NOT VERIFIED: too many tests ran (%d, limit %d); name fewer tests", leaves, browserLeafLimit)
	}
	return fmt.Sprintf("verified\nnamed test: %s passed (%d leaf tests)", run, leaves)
}
