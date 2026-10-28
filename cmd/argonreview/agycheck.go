package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
	"unicode"
)

// Observations are supplied facts, never inferred from a hook's exit status.
// In particular, silent exit 0 is a known allow, not a deny.
type agyCheckObservation struct {
	ID, Outcome string
	Loaded, Ran bool
	Summary     agyCheckSummary
}

// Reconciliation checks ordinary-agent regressions, not tamper resistance: an
// allow-everything agent can write the records. It is not proof for production,
// whose guard keeps no decision log. A match never approves version trust.
// Allows/denies count the alternating hook replies in claim-number order.
type agyCheckSummary struct {
	ToolSteps, HookInvocations, Allows, Denies, Errors, FilesCreated, ParallelBatches int
	// Totals alone cannot prove alternation; count adjacent identical hook replies.
	Repeats int
}

type agyCheckPin struct {
	Version, ExecutableSHA256 string
}

type agyCheckResultRow struct {
	agyCheckRow
	Observed string
	OK       bool
}

type agyCheckResult struct {
	InvalidReason, UsageError string
	Rows                      []agyCheckResultRow
	Unlisted                  []agyCheckLoad
	BuiltinInfo               string
}

type agyCheckLoad struct {
	Path      string
	Protected bool
}

func agyCheckLoadProtected(path string) bool {
	for _, part := range strings.Split(path, "/") {
		for _, refused := range implementRefusedNames {
			if strings.ToLower(part) == refused {
				return true
			}
		}
	}
	return false
}

var (
	agyCheckVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)
	agyCheckHashPattern    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

func agyCheckPinReason(before, after agyCheckPin) string {
	for _, pin := range []agyCheckPin{before, after} {
		if !agyCheckVersionPattern.MatchString(pin.Version) ||
			!agyCheckHashPattern.MatchString(pin.ExecutableSHA256) {
			return "empty or malformed version/executable SHA256 reading"
		}
	}
	if before.Version != after.Version {
		return "version changed"
	}
	if before.ExecutableSHA256 != after.ExecutableSHA256 {
		return "executable SHA256 changed"
	}
	return ""
}

func agyCheckClassify(observation agyCheckObservation, minimum int) agyCheckResultRow {
	for _, row := range agyCheckExpected {
		if row.ID == observation.ID {
			return agyCheckClassifyRow(row, observation, minimum)
		}
	}
	return agyCheckResultRow{agyCheckRow: agyCheckRow{ID: observation.ID}, Observed: "unknown row"}
}

func agyCheckClassifyRow(row agyCheckRow, o agyCheckObservation, minimum int) agyCheckResultRow {
	r := agyCheckResultRow{agyCheckRow: row, Observed: o.Outcome}
	if o.Outcome != "" && o.Outcome != row.Expected &&
		(o.Outcome == "not exercisable" || row.Expected == "covered if loaded" || row.Expected == "counts reconcile") {
		return r
	}
	switch row.Expected {
	case "allowed", "denied", "answered", "not exercisable":
		r.OK = o.Outcome == row.Expected
	case "covered if loaded":
		covered := agyCheckLoadProtected(row.Location)
		r.Observed = fmt.Sprintf("loaded=%t ran=%t covered=%t", o.Loaded, o.Ran, covered)
		r.OK = row.Location != "" && o.Loaded == o.Ran && (!o.Loaded || covered)
	case "counts reconcile":
		s := o.Summary
		r.Observed = fmt.Sprintf("steps=%d hooks=%d allows=%d denies=%d errors=%d files=%d batches=%d repeats=%d",
			s.ToolSteps, s.HookInvocations, s.Allows, s.Denies, s.Errors, s.FilesCreated, s.ParallelBatches, s.Repeats)
		// Either starting reply is possible; alternation needs both and a gap <= 1.
		r.OK = minimum > 0 && s.ToolSteps >= minimum && s.ParallelBatches >= 2 &&
			s.Allows > 0 && s.Denies > 0 && s.Allows-s.Denies >= -1 && s.Allows-s.Denies <= 1 &&
			s.Allows <= s.HookInvocations && s.Denies == s.HookInvocations-s.Allows &&
			s.ToolSteps == s.HookInvocations && s.Errors == s.Denies && s.FilesCreated == s.Allows && s.Repeats == 0
	default:
		r.Observed = "unknown expectation: " + row.Expected
	}
	return r
}

func agyCheckEvaluate(before, after agyCheckPin, observations []agyCheckObservation, minimum int, usageError string, loadedPaths ...string) agyCheckResult {
	r := agyCheckResult{UsageError: usageError}
	if usageError != "" {
		return r
	}
	if minimum <= 0 {
		r.UsageError = "minimum calls must be positive"
		return r
	}
	r.InvalidReason = agyCheckPinReason(before, after)
	if r.InvalidReason != "" {
		return r
	}
	counts := make(map[string]int)
	byID := make(map[string]agyCheckObservation)
	for _, o := range observations {
		counts[o.ID]++
		byID[o.ID] = o
	}
	for _, row := range agyCheckExpected {
		classified := agyCheckClassify(byID[row.ID], minimum)
		switch counts[row.ID] {
		case 0:
			classified = agyCheckResultRow{agyCheckRow: row, Observed: "missing row"}
		case 1:
		default:
			classified = agyCheckResultRow{agyCheckRow: row, Observed: "duplicate row"}
		}
		r.Rows = append(r.Rows, classified)
		delete(byID, row.ID)
	}
	// Preserve fixture order, including extra duplicates, for stable diagnostics.
	for _, o := range observations {
		if _, unknown := byID[o.ID]; unknown {
			r.Rows = append(r.Rows, agyCheckClassify(o, minimum))
		}
	}
	for _, path := range agyCheckObserveUnlistedLoads(loadedPaths) {
		protected := agyCheckLoadProtected(path)
		r.Unlisted = append(r.Unlisted, agyCheckLoad{path, protected})
		if !protected {
			r.Rows = append(r.Rows, agyCheckResultRow{agyCheckRow: agyCheckRow{ID: "unlisted-load", Expected: "protected", Description: "Loaded workspace hook outside the known table"}, Observed: "unprotected load"})
		}
	}
	return r
}

// Exit 0 means every observation equals the known expectation; it does not
// mean safe or trust approved. Usage errors have their own exit status.
func agyCheckExitCode(r agyCheckResult) int {
	if r.UsageError != "" {
		return agyCheckUsage
	}
	if r.InvalidReason != "" {
		return agyCheckDiffers
	}
	for _, load := range r.Unlisted {
		if !load.Protected {
			return agyCheckDiffers
		}
	}
	for _, row := range r.Rows {
		if !row.OK {
			return agyCheckDiffers
		}
	}
	return agyCheckSuccess
}

func agyCheckPrintable(value string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, value)
}

func agyCheckRender(w io.Writer, r agyCheckResult) error {
	var out strings.Builder
	if len(r.Unlisted) > 0 {
		fmt.Fprintln(&out, "LOADED BUT UNLISTED WORKSPACE HOOKS")
		for _, load := range r.Unlisted {
			protection := "UNPROTECTED: check differs"
			if load.Protected {
				protection = "PROTECTED by refused names"
			}
			fmt.Fprintf(&out, "%s: %s\n", protection, agyCheckPrintable(load.Path))
		}
	}
	if r.BuiltinInfo != "" {
		fmt.Fprintln(&out, r.BuiltinInfo)
	}
	switch {
	case r.UsageError != "":
		fmt.Fprintf(&out, "USAGE: %s\n", r.UsageError)
	case r.InvalidReason != "":
		fmt.Fprintf(&out, "RESULT INVALID: %s\n", r.InvalidReason)
	default:
		fmt.Fprintln(&out, "ID\tEXPECTED\tOBSERVED\tOK\tDESCRIPTION")
		differing := 0
		for _, row := range r.Rows {
			status := "ok"
			if !row.OK {
				status = "differs"
				differing++
			}
			fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\n", agyCheckPrintable(row.ID), row.Expected, agyCheckPrintable(row.Observed), status, row.Description)
		}
		fmt.Fprintf(&out, "Rows: %d; differing: %d\n", len(r.Rows), differing)
		if agyCheckExitCode(r) == agyCheckSuccess {
			out.WriteString(agyCheckSuccessLine + "\n")
		}
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	n, err := io.WriteString(table, out.String())
	if err == nil && n != out.Len() {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = table.Flush()
	}
	if err != nil {
		return fmt.Errorf("writing agy-check report: %w", err)
	}
	return nil
}
