package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func commandVerdict(code int, err error, truncated bool) string {
	if code != 0 || err != nil {
		return "FAILED"
	}
	if truncated {
		return "NOT VERIFIED: output limit exceeded (output cut off)"
	}
	return "verified"
}

type containerTestEvent struct {
	Action, Package, Test, Output, ImportPath string
}

type containerTestPackage struct {
	final  string
	tests  map[string]string
	output map[string]string
}

// testVerdict is a pure reading of test2json, independent of build/vet. Every
// observed package and test must finish. Package passes and parent tests never
// inflate the leaf pass count, and a failed package cannot hide behind a child
// pass (build failures and panics may have no failed test event at all).
func testVerdict(code int, runErr error, data []byte, truncated bool) string {
	return testVerdictWithAllowlist(code, runErr, data, truncated, nil)
}

func testVerdictWithAllowlist(code int, runErr error, data []byte, truncated bool, allowlist []measurementSkip) string {
	if code != 0 || runErr != nil {
		return "FAILED"
	}
	if truncated {
		return "NOT VERIFIED: output limit exceeded (output cut off)"
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return "FAILED: empty or truncated test stream"
	}
	packages := map[string]*containerTestPackage{}
	failed, noTests := false, false
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		if err := distinctKeys(line); err != nil {
			return "FAILED: malformed test stream"
		}
		var e containerTestEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return "FAILED: malformed test stream"
		}
		if e.Action == "build-fail" || e.Action == "build-output" {
			// A compiler failure has ImportPath instead of Package/Test in recent
			// Go versions. These events can never supply test pass evidence.
			failed = true
			continue
		}
		if e.Package == "" {
			return "FAILED: event without package"
		}
		p := packages[e.Package]
		if e.Action == "start" {
			if p != nil || e.Test != "" {
				return "FAILED: duplicate or invalid package start"
			}
			packages[e.Package] = &containerTestPackage{tests: map[string]string{}, output: map[string]string{}}
			continue
		}
		if p == nil || p.final != "" {
			return "FAILED: event outside a running package"
		}
		switch e.Action {
		case "run":
			if e.Test == "" {
				return "FAILED: test without name"
			}
			if _, ok := p.tests[e.Test]; ok {
				return "FAILED: duplicate test"
			}
			p.tests[e.Test] = ""
		case "pause", "cont":
			if state, ok := p.tests[e.Test]; !ok || state != "" {
				return "FAILED: event outside a running test"
			}
		case "output":
			if e.Test != "" {
				p.output[e.Test] += e.Output
			}
			if strings.Contains(e.Output, "[no test files]") {
				noTests = true
			}
			if strings.Contains(e.Output, "[build failed]") || strings.Contains(e.Output, "panic:") || strings.Contains(e.Output, "test timed out") {
				failed = true
			}
		case "pass", "fail", "skip":
			if e.Action == "fail" {
				failed = true
			}
			if e.Test == "" {
				p.final = e.Action
			} else {
				if state, ok := p.tests[e.Test]; !ok || state != "" {
					return "FAILED: result without a running test"
				}
				p.tests[e.Test] = e.Action
			}
		default:
			return "FAILED: unknown test action"
		}
	}
	if failed {
		return "FAILED"
	}
	passing := 0
	var skipped, allowed []string
	for name, p := range packages {
		if len(p.tests) == 0 {
			noTests = true
		}
		if p.final == "" {
			return "FAILED: missing package result"
		}
		if p.final == "skip" {
			skipped = append(skipped, name+" (package)")
		}
		parents := map[string]bool{}
		for test := range p.tests {
			for parent := test; ; {
				i := strings.LastIndexByte(parent, '/')
				if i < 0 {
					break
				}
				parent = parent[:i]
				parents[parent] = true
			}
		}
		packagePassing := 0
		for test, state := range p.tests {
			if !parents[test] && state == "pass" {
				packagePassing++
			}
		}
		for test, state := range p.tests {
			if state == "" {
				return "FAILED: missing test result"
			}
			// A parent is not a leaf even when a descendant has no result; the
			// incomplete descendant above makes the whole stream fail closed.
			leaf := !parents[test]
			if state == "skip" {
				if allowedMeasurementSkip(allowlist, name, test, p.output[test], parents[test], packagePassing) {
					allowed = append(allowed, name+"/"+test)
				} else {
					skipped = append(skipped, name+"/"+test)
				}
			}
			if leaf && state == "pass" {
				passing++
			}
		}
	}
	if len(skipped) != 0 {
		sort.Strings(skipped)
		return fmt.Sprintf("NOT VERIFIED: %d skipped\n%s", len(skipped), strings.Join(skipped, "\n"))
	}
	if noTests || passing == 0 {
		return "NOT VERIFIED: a targeted package has no tests, or no leaf test passed"
	}
	report := fmt.Sprintf("verified\n%d passing leaf tests", passing)
	if len(allowed) != 0 {
		sort.Strings(allowed)
		report += "\nallowed measurement skips: " + strings.Join(allowed, ", ")
	}
	return report
}
