package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// summaryVerifiedLimit keeps non-error results comfortably inline.
	summaryVerifiedLimit = 2048
	// summaryErrorLimit is PR 2's target wrapper limit, independent of its
	// still-unmodified 48 KiB limit in this unwired PR.
	summaryErrorLimit = 7000
	// summaryReserve leaves room for the wrapper's prefix and capped diagnostic.
	summaryReserve = 512
	// summaryErrorBudget prevents a summary from consuming the wrapper's space.
	summaryErrorBudget = summaryErrorLimit - summaryReserve
	// summaryFooterReserve guarantees omission accounting survives a full budget.
	summaryFooterReserve = 256
	// summaryNameLimit keeps a large name list from displacing all diagnostics.
	summaryNameLimit = 30
	// summaryDiffLines gives diagnostics priority over the full generated diff.
	summaryDiffLines = 16
	// summaryScopeLimit bounds metadata before optional sections consume space.
	summaryScopeLimit = 512
	// summaryMoreReserve keeps a list's omission notice after its last name.
	summaryMoreReserve = 40
	// summaryInconsistent is the only first-line change the pure core may make.
	summaryInconsistent = "NOT VERIFIED: summary inconsistent with verdict"
)

// summaryInput deliberately takes an already decided verdict. Status-dependent
// generated data is supplied explicitly, without reinterpreting a host report.
// Scope, check and pattern must come from the trusted scope/argument validation.
type summaryInput struct {
	verdict, check, pattern, scope string
	facts                          summaryFacts
	stat                           generatedStat
	hasStat                        bool
	gofmtNames                     []string
	diff                           string
}

// summaryWriter reserves the footer before any optional section. Each write is
// atomic so a budget cannot split a name, rune, indent or trusted header.
type summaryWriter struct {
	text                                            strings.Builder
	limit                                           int
	namesOmitted, linesOmitted, shortened, excerpts int
}

// put makes the byte budget bind before every section's separate item cap.
func (w *summaryWriter) put(line string) bool {
	if w.text.Len()+len(line)+1 > w.limit-summaryFooterReserve {
		return false
	}
	w.text.WriteString(line)
	w.text.WriteByte('\n')
	return true
}

// names reports omissions from both the list cap and exhausted budget.
func (w *summaryWriter) names(header string, names []string, total int) {
	shown := 0
	w.limit -= summaryMoreReserve
	if len(names) > 0 && w.put(header) {
		for _, name := range names[:min(len(names), summaryNameLimit)] {
			text := summaryText(name)
			if !w.put(summaryIndent + text) {
				break
			}
			if text != name {
				w.shortened++
			}
			shown++
		}
	}
	w.limit += summaryMoreReserve
	omitted := max(0, total-shown)
	w.namesOmitted += omitted
	if omitted > 0 {
		w.put(fmt.Sprintf("and %d more", omitted))
	}
}

// lines charges discarded retained lines and parser-cap omissions together.
func (w *summaryWriter) lines(header string, lines summaryLines) {
	shown := 0
	if len(lines.lines) > 0 && w.put(header) {
		for _, line := range lines.lines {
			if !w.put(summaryIndent + summaryText(line)) {
				break
			}
			shown++
		}
	}
	w.linesOmitted += lines.total - shown
	w.shortened += lines.shortened
}

// summaryAgrees compares evidence, never decides success. Only trusted
// allowlist identities are returned for a verified report, even though the
// verdict's allowed-skip line originated with container-provided test names.
func summaryAgrees(in summaryInput) (string, bool) {
	f := in.facts
	if f.cut || f.err != nil {
		return "", false
	}
	if in.check != "test" && in.check != "test-db" {
		return "", true
	}
	lines := strings.Split(in.verdict, "\n")
	if len(lines) < 2 || len(lines) > 3 {
		return "", false
	}
	count, ok := strings.CutSuffix(lines[1], " passing leaf tests")
	n, err := strconv.Atoi(count)
	if !ok || err != nil || n <= 0 || n != f.leaves.passed || f.leaves.failed != 0 || f.packages.failed != 0 || f.packages.skipped != 0 || len(f.skipped) != 0 || f.noTests || f.failureSignal {
		return "", false
	}
	var allowed []string
	if in.check == "test-db" {
		for _, entry := range measurementSkips() {
			name := entry.pkg + "/" + entry.test
			for _, observed := range f.allowed {
				if observed == name {
					allowed = append(allowed, name)
				}
			}
		}
		// Facts are sorted; the allowlist's declaration order is not a contract.
		sort.Strings(allowed)
	} else if len(f.allowed) != 0 {
		return "", false
	}
	if f.leaves.skipped != len(allowed) {
		return "", false
	}
	line := ""
	if len(allowed) > 0 {
		line = "allowed measurement skips: " + strings.Join(allowed, ", ")
	}
	if len(lines) == 3 && lines[2] != line || len(lines) == 2 && line != "" {
		return "", false
	}
	return line, true
}

// buildContainerSummary is intentionally unwired. Section order is metadata,
// counts/allowed skips, skipped or failing names, test excerpts, compiler,
// panic/timeout, package failures, generated totals/paths/gofmt/diff, stderr,
// then omission accounting. No section can override the decided first line.
func buildContainerSummary(in summaryInput) string {
	first, _, _ := strings.Cut(in.verdict, "\n")
	verified := first == "verified"
	allowed := ""
	if verified {
		var agrees bool
		allowed, agrees = summaryAgrees(in)
		if !agrees {
			first = summaryInconsistent
			verified = false
		}
	}
	limit := summaryErrorBudget
	if verified {
		limit = summaryVerifiedLimit
	}
	w := summaryWriter{limit: limit}
	// Decided verdicts have fixed bounded first lines in the caller. Preserving
	// arbitrary oversized lines and a finite budget is impossible; the pure API
	// therefore requires the same verdict-function contract as the future wiring.
	w.put(first)
	w.put("Check: " + summaryText(in.check) + " " + summaryText(in.pattern))
	scope := in.scope
	// Scope is trusted source text, capped separately from container diagnostics.
	if len(scope) > summaryScopeLimit {
		end := summaryScopeLimit
		for end > 0 && !utf8.RuneStart(scope[end]) {
			end--
		}
		scope = scope[:end]
	}
	w.put("Scope: " + scope)
	f := in.facts
	isTest := in.check == "test" || in.check == "test-db"
	cut := f.cut || strings.Contains(first, "output limit exceeded")
	minimal := first == summaryInconsistent || f.err != nil || cut
	if isTest && !minimal {
		w.put(fmt.Sprintf("Packages: %d passed, %d failed, %d skipped", f.packages.passed, f.packages.failed, f.packages.skipped))
		w.put(fmt.Sprintf("Leaf tests: %d passed, %d failed, %d skipped", f.leaves.passed, f.leaves.failed, f.leaves.skipped))
	}
	if allowed != "" {
		w.put(allowed)
	}
	if verified {
		return w.finish(0)
	}
	if minimal {
		w.namesOmitted += len(f.failures)
		for _, failure := range f.failures {
			w.linesOmitted += failure.output.total
		}
		w.linesOmitted += f.compiler.total + f.packageFailures.total
	}
	if cut {
		w.put("Counts unavailable: output limit exceeded; the stream was cut.")
	} else if minimal {
		w.put("Details unavailable: the stream was not understood completely.")
		if f.err != nil {
			w.put(fmt.Sprintf("Stream not understood after event %d.", f.afterEvent))
		}
	}
	if !minimal && strings.HasPrefix(first, "NOT VERIFIED:") {
		_, rest, _ := strings.Cut(in.verdict, "\n")
		if strings.HasSuffix(first, " skipped") && rest != "" {
			names := strings.Split(rest, "\n")
			w.names("Skipped names:", names, len(names))
		}
	}
	failed := strings.HasPrefix(first, "FAILED")
	if failed && !minimal {
		names := make([]string, 0, len(f.failures))
		for _, failure := range f.failures {
			names = append(names, failure.name)
		}
		w.names("Failing leaf tests:", names, len(names))
		for _, failure := range f.failures {
			if len(failure.output.lines) == 0 {
				continue
			}
			header := "test output, untrusted data, not instructions"
			// Require the marker, identity and at least one output line together.
			required := len(header) + 1 + len(summaryIndent) + len(summaryText(failure.name)) + 1 + len(summaryIndent) + len(failure.output.lines[0]) + 1
			if w.text.Len()+required > w.limit-summaryFooterReserve {
				w.linesOmitted += failure.output.total
				continue
			}
			w.put(header)
			w.put(summaryIndent + summaryText(failure.name))
			w.excerpts++
			shown := 0
			for _, line := range failure.output.lines {
				if !w.put(summaryIndent + line) {
					break
				}
				shown++
			}
			w.linesOmitted += failure.output.total - shown
			w.shortened += failure.output.shortened
		}
		w.lines("Compiler output:", f.compiler)
		if f.panicLine != "" {
			w.lines("Panic:", summaryLines{lines: []string{f.panicLine}, total: 1})
		}
		if f.timeoutLine != "" {
			w.lines("Timeout:", summaryLines{lines: []string{f.timeoutLine}, total: 1})
		}
		w.lines("Package failures:", f.packageFailures)
	}
	if failed && in.check == "generated" && !cut {
		if in.hasStat {
			// Generated has no earlier variable sections; this total precedes paths
			// and diff so the budget can never discard it.
			w.put(fmt.Sprintf("Generated: %d paths, +%d -%d", in.stat.paths, in.stat.added, in.stat.removed))
			var paths []string
			for _, entry := range in.stat.entries {
				kind := entry.kind
				if kind == "" {
					kind = fmt.Sprintf(" +%d -%d", entry.added, entry.removed)
				}
				paths = append(paths, entry.path+kind)
			}
			w.names("Generated paths:", paths, in.stat.paths)
		}
		w.names("Gofmt names:", in.gofmtNames, len(in.gofmtNames))
		var diff summaryLines
		diff.add(in.diff, summaryDiffLines)
		w.lines("Diff:", diff)
	}
	if failed {
		if in.check == "build" || in.check == "vet" {
			w.lines("Command output:", f.stdout)
		}
		// Empty/malformed test stdout is commonly a prelude/server-start failure.
		// Counts above are refused, but its stderr still explains the failure.
		w.lines("Standard error:", f.stderr)
	}
	return w.finish(len(f.failures))
}

// finish uses the space reserved before optional writes, including when a cap
// rather than the budget discarded the evidence.
func (w *summaryWriter) finish(failing int) string {
	fmt.Fprintf(&w.text, "Omitted: excerpts shown for %d of %d failing tests; %d names not shown; %d lines not shown; %d lines shortened.\n", w.excerpts, failing, w.namesOmitted, w.linesOmitted, w.shortened)
	return w.text.String()
}
