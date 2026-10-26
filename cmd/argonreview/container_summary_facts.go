package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// summaryTestLimit bounds retained identities even when output is tiny.
	summaryTestLimit = 20000
	// summaryPackageLimit bounds package maps independently of an event flood.
	summaryPackageLimit = 4096
	// summaryEventLimit bounds JSON decoding work and temporary allocations.
	summaryEventLimit = 256 << 10
	// summaryIdentityLimit bounds each identity and slash-ancestor computation.
	summaryIdentityLimit = 4096
	// summaryFieldLimit bounds duplicate-key comparisons in a wide JSON object.
	summaryFieldLimit = 32
	// summaryExcerptLimit bounds retained output even for passing noisy tests.
	summaryExcerptLimit = 8
)

// summaryCounts excludes parent tests so subtests cannot inflate evidence.
type summaryCounts struct{ passed, failed, skipped int }

// summaryTest retains bounded excerpts and a rolling message suffix: an allowed
// skip message may occur late, or straddle output events, in an honest stream.
type summaryTest struct {
	state   string
	output  summaryLines
	tail    string
	message bool
}

// summaryPackage keeps protocol state separate from the decided verdict.
type summaryPackage struct {
	final   string
	tests   map[string]*summaryTest
	parents map[string]bool
}

// summaryFailure keeps raw identities for sorting, never for direct printing.
type summaryFailure struct {
	name   string
	output summaryLines
}

// summaryFacts is evidence only. A partial parse must never supply success or
// counts; afterEvent records the first event boundary the parser did not trust.
type summaryFacts struct {
	packages, leaves                          summaryCounts
	failures                                  []summaryFailure
	allowed, skipped                          []string
	compiler, packageFailures, stderr, stdout summaryLines
	panicLine, timeoutLine                    string
	events, afterEvent                        int
	err                                       error
	cut                                       bool
	noTests, failureSignal                    bool
}

// problem preserves the first loss of understanding while later diagnostics
// continue to be collected. Error text itself is never printed in the summary.
func (f *summaryFacts) problem(err error) {
	if f.err == nil {
		f.err = fmt.Errorf("reading summary facts after event %d: %w", f.events, err)
		f.afterEvent = f.events
	}
}

// summaryEvent checks duplicate keys with a small field cap before using Go's
// case-insensitive decoding. This avoids quadratic work on hostile wide objects.
func summaryEvent(line []byte) (containerTestEvent, error) {
	var e containerTestEvent
	d := json.NewDecoder(bytes.NewReader(line))
	token, err := d.Token()
	if err != nil {
		return e, fmt.Errorf("reading event object: %w", err)
	}
	if token != json.Delim('{') {
		return e, errors.New("event is not an object")
	}
	var keys []string
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return e, fmt.Errorf("reading event key: %w", err)
		}
		key, ok := token.(string)
		if !ok || len(keys) == summaryFieldLimit {
			return e, errors.New("event fields exceed limit")
		}
		for _, previous := range keys {
			if strings.EqualFold(previous, key) {
				return e, errors.New("duplicate event field")
			}
		}
		keys = append(keys, key)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return e, fmt.Errorf("reading event value: %w", err)
		}
	}
	if err := json.Unmarshal(line, &e); err != nil {
		return e, fmt.Errorf("decoding event: %w", err)
	}
	return e, nil
}

// parseSummaryFacts tolerates nonzero exits and keeps scanning after bad events.
// It has no exit status and cannot decide a verdict. Map work is linear in the
// input and its bounded identities; no output is repeatedly concatenated.
func parseSummaryFacts(check string, stdout, stderr []byte, cut bool) summaryFacts {
	f := summaryFacts{cut: cut}
	f.stderr.add(string(stderr), summaryExcerptLimit)
	f.stdout.add(string(stdout), summaryExcerptLimit)
	if check != "test" && check != "test-db" {
		f.notice(string(stdout))
		f.notice(string(stderr))
		return f
	}
	packages := map[string]*summaryPackage{}
	tests := 0
	allowlist := measurementSkips()
	if len(stdout) == 0 || stdout[len(stdout)-1] != '\n' {
		f.problem(errors.New("empty or truncated stream"))
	}
	for line := range bytes.SplitSeq(bytes.TrimSuffix(stdout, []byte("\n")), []byte("\n")) {
		f.events++
		if len(line) > summaryEventLimit {
			f.problem(errors.New("event exceeds limit"))
			continue
		}
		e, err := summaryEvent(line)
		if err != nil {
			f.problem(err)
			continue
		}
		if len(e.Package) > summaryIdentityLimit || len(e.Test) > summaryIdentityLimit {
			f.problem(errors.New("identity exceeds limit"))
			continue
		}
		f.notice(e.Output)
		if e.Action == "build-output" || e.Action == "build-fail" {
			f.failureSignal = true
			if e.Action == "build-output" {
				f.compiler.fragment(e.Output, summaryExcerptLimit)
			} else {
				f.packageFailures.add(e.ImportPath+" [build failed]", summaryExcerptLimit)
			}
			continue
		}
		if strings.Contains(e.Output, "[no test files]") {
			f.noTests = true
		}
		if strings.Contains(e.Output, "panic:") || strings.Contains(e.Output, "test timed out") || strings.Contains(e.Output, "[build failed]") {
			f.failureSignal = true
		}
		p := packages[e.Package]
		if e.Package == "" {
			f.problem(errors.New("event without package"))
			continue
		}
		if e.Action == "start" {
			if p != nil || e.Test != "" || len(packages) == summaryPackageLimit {
				f.problem(errors.New("invalid package start or retention limit"))
				continue
			}
			packages[e.Package] = &summaryPackage{tests: map[string]*summaryTest{}, parents: map[string]bool{}}
			continue
		}
		if p == nil || p.final != "" {
			f.problem(errors.New("event outside running package"))
			continue
		}
		test := p.tests[e.Test]
		switch e.Action {
		case "run":
			if e.Test == "" || test != nil || tests == summaryTestLimit {
				f.problem(errors.New("invalid test start or retention limit"))
				continue
			}
			tests++
			p.tests[e.Test] = &summaryTest{}
			// Mirror testVerdictWithAllowlist: every slash ancestor is a parent,
			// including ancestors that have no explicit run event.
			for parent := e.Test; ; {
				i := strings.LastIndexByte(parent, '/')
				if i < 0 {
					break
				}
				parent = parent[:i]
				p.parents[parent] = true
			}
		case "pause", "cont":
			if test == nil || test.state != "" {
				f.problem(errors.New("event outside running test"))
			}
		case "output":
			if e.Test == "" && (strings.Contains(e.Output, "FAIL") || strings.Contains(e.Output, "[build failed]")) {
				f.packageFailures.add(e.Output, summaryExcerptLimit)
			}
			if test != nil {
				test.output.fragment(e.Output, summaryExcerptLimit)
				for _, entry := range allowlist {
					if entry.pkg == e.Package && entry.test == e.Test {
						combined := test.tail + e.Output
						test.message = test.message || strings.Contains(combined, entry.message)
						test.tail = strings.Clone(combined[max(0, len(combined)-len(entry.message)):])
					}
				}
			}
		case "pass", "fail", "skip":
			if e.Action == "fail" {
				f.failureSignal = true
			}
			if e.Test == "" {
				p.final = e.Action
				if e.Action == "fail" {
					f.packageFailures.add(e.Package+": package failed", summaryExcerptLimit)
				}
			} else if test == nil || test.state != "" {
				f.problem(errors.New("result without running test"))
			} else {
				test.state = e.Action
			}
		default:
			f.problem(errors.New("unknown action"))
		}
	}
	for name, p := range packages {
		if len(p.tests) == 0 {
			f.noTests = true
		}
		if p.final == "" {
			f.problem(errors.New("missing package result"))
		}
		summaryCount(&f.packages, p.final)
		passing := 0
		for name, test := range p.tests {
			if !p.parents[name] && test.state == "pass" {
				passing++
			}
		}
		if p.final == "skip" {
			f.skipped = append(f.skipped, name+" (package)")
		}
		for testName, test := range p.tests {
			test.output.end(summaryExcerptLimit)
			if test.state == "" {
				f.problem(errors.New("missing test result"))
			}
			leaf := !p.parents[testName]
			if leaf {
				summaryCount(&f.leaves, test.state)
			}
			if leaf && test.state == "fail" {
				f.failures = append(f.failures, summaryFailure{name + "/" + testName, test.output})
			}
			if test.state == "skip" {
				allowed := false
				for _, entry := range allowlist {
					if entry.pkg == name && entry.test == testName && test.message && allowedMeasurementSkip(allowlist, name, testName, entry.message, !leaf, passing) {
						allowed = true
					}
				}
				if allowed {
					f.allowed = append(f.allowed, name+"/"+testName)
				} else {
					f.skipped = append(f.skipped, name+"/"+testName)
				}
			}
		}
	}
	f.compiler.end(summaryExcerptLimit)
	f.notice(string(stderr))
	sort.Slice(f.failures, func(i, j int) bool { return f.failures[i].name < f.failures[j].name })
	sort.Strings(f.allowed)
	sort.Strings(f.skipped)
	return f
}

// summaryCount handles only final states; unfinished work is never a pass.
func summaryCount(count *summaryCounts, state string) {
	switch state {
	case "pass":
		count.passed++
	case "fail":
		count.failed++
	case "skip":
		count.skipped++
	}
}

// notice scans even discarded lines for the first panic and timeout. Stdout
// event order wins; stderr supplies a fallback because cross-stream ordering
// is not available in captured buffers.
func (f *summaryFacts) notice(output string) {
	for line := range strings.SplitSeq(output, "\n") {
		if f.panicLine == "" && strings.Contains(line, "panic:") {
			f.panicLine = summaryText(line)
		}
		if f.timeoutLine == "" && strings.Contains(line, "test timed out") {
			f.timeoutLine = summaryText(line)
		}
	}
}
