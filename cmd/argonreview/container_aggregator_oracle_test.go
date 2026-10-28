package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// oracleRunVerdict applies only #1857's runner-return and late-context rules
// around the frozen parser below; its stream decisions remain verbatim.
func oracleRunVerdict(code int, runErr error, data []byte, state aggregatorRunState) string {
	verdict := oracleVerdict(code, runErr, data, false, nil)
	if state.deadline && state.interrupted {
		return containerTimeLimitVerdict
	}
	if state.parentCancelled {
		return "FAILED"
	}
	if state.deadline && state.completed && code != 125 {
		verdict = oracleVerdict(code, nil, data, false, nil)
		if strings.HasPrefix(verdict, "verified\n") {
			return containerTimeLimitVerdict
		}
	}
	return verdict
}

// oracleCompleteFacts is used only after both decisions understood a complete
// stream. It derives counts independently of the production summary parser,
// whose older test cap must not restrict this differential oracle.
func oracleCompleteFacts(data []byte, allowlist []measurementSkip) (summaryFacts, int) {
	var f summaryFacts
	confirmed := 0
	packages := map[string]*containerTestPackage{}
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var e containerTestEvent
		_ = json.Unmarshal(line, &e)
		f.events++
		if e.Action == "fail" || e.Action == "build-fail" {
			confirmed++
		}
		if e.Action == "build-fail" || e.Action == "build-output" {
			continue
		}
		if e.Action == "start" {
			packages[e.Package] = &containerTestPackage{tests: map[string]string{}, output: map[string]string{}}
			continue
		}
		p := packages[e.Package]
		switch e.Action {
		case "run":
			p.tests[e.Test] = ""
		case "output":
			p.output[e.Test] += e.Output
		case "pass", "skip", "fail":
			if e.Test == "" {
				p.final = e.Action
			} else {
				p.tests[e.Test] = e.Action
			}
		}
	}
	for pkg, p := range packages {
		summaryCount(&f.packages, p.final)
		parents := map[string]bool{}
		for name := range p.tests {
			for parent := name; ; {
				i := strings.LastIndexByte(parent, '/')
				if i < 0 {
					break
				}
				parent = parent[:i]
				parents[parent] = true
			}
		}
		passing := 0
		for name, state := range p.tests {
			if !parents[name] && state == "pass" {
				passing++
			}
		}
		if p.final == "skip" {
			f.skipped = append(f.skipped, pkg+" (package)")
		}
		for name, state := range p.tests {
			if !parents[name] {
				summaryCount(&f.leaves, state)
			}
			if state == "skip" {
				if allowedMeasurementSkip(allowlist, pkg, name, p.output[name], parents[name], passing) {
					f.allowed = append(f.allowed, pkg+"/"+name)
				} else {
					f.skipped = append(f.skipped, pkg+"/"+name)
				}
			}
		}
	}
	sort.Strings(f.allowed)
	sort.Strings(f.skipped)
	return f, confirmed
}

// oracleVerdict is the frozen pre-aggregation decision, kept independently
// while production callers still require the original unwired implementation.
func oracleVerdict(code int, runErr error, data []byte, truncated bool, allowlist []measurementSkip) string {
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
