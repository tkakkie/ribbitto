package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"unicode"
)

// Independent fixtures pin both the row set and the known outcomes.
const agyCheckFixtureRows = `hook-decision=denied hook-exit2=denied hook-exit1=denied hook-missing=denied hook-timeout=denied hook-unparseable=denied hook-silent=allowed
discovery-agent=covered_if_loaded discovery-agents=covered_if_loaded discovery-gemini-config=covered_if_loaded discovery-gemini=covered_if_loaded discovery-antigravity=covered_if_loaded discovery-root=covered_if_loaded
hook-precedence=denied hook-same-name-agent=denied hook-same-name-agents=denied command-on=denied command-off=denied mcp-on=denied mcp-off=denied read-url=not_exercisable execute-url=not_exercisable
task-on=denied task-off=allowed list-resources=answered read-resource=answered outside-write=allowed outside-read=allowed reconciliation=counts_reconcile parallel-batches=sequential`

func agyCheckFixtures() (agyCheckPin, []agyCheckObservation) {
	var observations []agyCheckObservation
	for _, pair := range strings.Fields(agyCheckFixtureRows) {
		id, outcome, _ := strings.Cut(pair, "=")
		observations = append(observations, agyCheckObservation{ID: id, Outcome: strings.ReplaceAll(outcome, "_", " "),
			Loaded: true, Ran: true, Summary: agyCheckSummary{30, 30, 15, 15, 15, 15, 0, 0}})
	}
	return agyCheckPin{"1.4.7", strings.Repeat("a", 64)}, observations
}

func TestAgyCheckExpectedTable(t *testing.T) {
	_, fixtures := agyCheckFixtures()
	if len(agyCheckExpected) != 30 || len(fixtures) != len(agyCheckExpected) || agyCheckDefaultCalls != 30 {
		t.Fatal("row set or default calls changed")
	}
	seen := make(map[string]bool)
	locations := strings.Fields(".agent/hooks.json .agents/hooks.json .gemini/config/hooks.json .gemini/hooks.json .antigravity/hooks.json hooks.json")
	for i, row := range agyCheckExpected {
		if seen[row.ID] || row.ID != fixtures[i].ID || row.Expected != fixtures[i].Outcome || row.Group == "" || row.Description == "" {
			t.Fatalf("row %d drifted: %+v", i, row)
		}
		seen[row.ID] = true
		switch row.Expected {
		case "allowed", "denied", "answered", "not exercisable", "covered if loaded", "counts reconcile", "sequential":
		default:
			t.Fatalf("unrecognised expectation: %s", row.Expected)
		}
		if row.Group == "discovery" {
			if len(locations) == 0 || row.Location != locations[0] {
				t.Fatalf("candidate drifted: %+v", row)
			}
			locations = locations[1:]
		}
	}
	if len(locations) != 0 {
		t.Fatal("missing discovery candidates")
	}
}

func TestAgyCheckSilentExitZeroAllows(t *testing.T) {
	for _, outcome := range []string{"allowed", "denied"} {
		t.Run(outcome, func(t *testing.T) {
			r := agyCheckClassify(agyCheckObservation{ID: "hook-silent", Outcome: outcome}, agyCheckDefaultCalls)
			if r.Observed != outcome || r.OK != (outcome == "allowed") {
				t.Fatalf("silent exit 0: %+v", r)
			}
		})
	}
}

func TestAgyCheckClassification(t *testing.T) {
	_, fixtures := agyCheckFixtures()
	for _, o := range fixtures {
		t.Run(o.ID, func(t *testing.T) {
			if r := agyCheckClassify(o, agyCheckDefaultCalls); !r.OK {
				t.Fatalf("fixture differs: %+v", r)
			}
			for _, outcome := range []string{"allowed", "denied", "answered", "not exercisable", "", "unknown"} {
				if (o.Outcome == "covered if loaded" || o.Outcome == "counts reconcile") && outcome != "not exercisable" {
					continue
				}
				changed := o
				changed.Outcome = outcome
				if r := agyCheckClassify(changed, agyCheckDefaultCalls); r.OK != (outcome == o.Outcome) {
					t.Fatalf("outcome %q: %+v", outcome, r)
				}
			}
		})
	}
	if agyCheckClassify(agyCheckObservation{ID: "unknown"}, agyCheckDefaultCalls).OK ||
		agyCheckClassifyRow(agyCheckRow{Expected: "unknown"}, agyCheckObservation{Outcome: "unknown"}, agyCheckDefaultCalls).OK {
		t.Fatal("unknown row/expectation matched")
	}
}

func TestAgyCheckDiscoveryRefusedNames(t *testing.T) {
	for _, name := range append(append([]string{}, implementRefusedNames...), "ordinary") {
		t.Run(name, func(t *testing.T) {
			row := agyCheckRow{Expected: "covered if loaded", Location: "nested/" + strings.ToUpper(name) + "/custom"}
			for _, facts := range []struct{ loaded, ran, ok bool }{{true, true, name != "ordinary"}, {false, false, true}, {true, false, false}, {false, true, false}} {
				r := agyCheckClassifyRow(row, agyCheckObservation{Loaded: facts.loaded, Ran: facts.ran}, agyCheckDefaultCalls)
				if r.OK != facts.ok {
					t.Fatalf("%+v: %+v", facts, r)
				}
			}
		})
	}
}

func TestAgyCheckDiscoveryContradictingOutcome(t *testing.T) {
	for _, facts := range []struct {
		name   string
		loaded bool
	}{
		{"unloaded", false},
		{"loaded", true},
	} {
		t.Run(facts.name, func(t *testing.T) {
			for _, outcome := range []string{"error", "allowed", "unknown", "denied", "answered", "not exercisable", "counts reconcile", "covered if loaded", ""} {
				t.Run(outcome, func(t *testing.T) {
					r := agyCheckClassify(agyCheckObservation{ID: "discovery-agent", Outcome: outcome,
						Loaded: facts.loaded, Ran: facts.loaded}, agyCheckDefaultCalls)
					wantOK := outcome == "" || outcome == "covered if loaded"
					if r.OK != wantOK {
						t.Errorf("outcome %q: %+v", outcome, r)
					}
					if !wantOK && r.Observed != outcome {
						t.Errorf("outcome %q replaced by %q", outcome, r.Observed)
					}
				})
			}
		})
	}
}

func TestAgyCheckDiscoveryEmptyLocation(t *testing.T) {
	r := agyCheckClassifyRow(agyCheckRow{Expected: "covered if loaded"}, agyCheckObservation{Loaded: false, Ran: false}, agyCheckDefaultCalls)
	if r.OK {
		t.Fatalf("empty location matched: %+v", r)
	}
}

func TestAgyCheckReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary agyCheckSummary
		minimum int
		ok      bool
	}{
		{"default", agyCheckSummary{30, 30, 15, 15, 15, 15, 2, 0}, agyCheckDefaultCalls, true},
		{"odd", agyCheckSummary{31, 31, 16, 15, 15, 16, 2, 0}, 31, true},
		{"override", agyCheckSummary{40, 40, 20, 20, 20, 20, 2, 0}, 40, true},
		{"too few", agyCheckSummary{28, 28, 14, 14, 14, 14, 2, 0}, agyCheckDefaultCalls, false},
		{"override too few", agyCheckSummary{30, 30, 15, 15, 15, 15, 2, 0}, 40, false},
		{"parallel", agyCheckSummary{30, 30, 15, 15, 15, 15, 1, 0}, agyCheckDefaultCalls, true},
		{"sequential", agyCheckSummary{30, 30, 15, 15, 15, 15, 0, 0}, agyCheckDefaultCalls, true},
		{"steps", agyCheckSummary{31, 30, 15, 15, 15, 15, 2, 0}, agyCheckDefaultCalls, false},
		{"hooks", agyCheckSummary{30, 31, 15, 15, 15, 15, 2, 0}, agyCheckDefaultCalls, false},
		{"errors", agyCheckSummary{30, 30, 15, 15, 14, 15, 2, 0}, agyCheckDefaultCalls, false},
		{"files", agyCheckSummary{30, 30, 15, 15, 15, 14, 2, 0}, agyCheckDefaultCalls, false},
		{"total", agyCheckSummary{30, 30, 14, 15, 15, 14, 2, 0}, agyCheckDefaultCalls, false},
		{"reverse alternation", agyCheckSummary{30, 30, 13, 17, 17, 13, 0, 0}, 30, false},
		{"zero minimum", agyCheckSummary{30, 30, 15, 15, 15, 15, 0, 0}, 0, false},
		{"alternation", agyCheckSummary{30, 30, 17, 13, 13, 17, 2, 0}, agyCheckDefaultCalls, false},
		{"repeats", agyCheckSummary{30, 30, 15, 15, 15, 15, 2, 28}, agyCheckDefaultCalls, false},
		{"negative", agyCheckSummary{30, 30, -1, 31, 31, -1, 2, 0}, agyCheckDefaultCalls, false},
		{"no allows", agyCheckSummary{1, 1, 0, 1, 1, 0, 2, 0}, 1, false},
		{"no denies", agyCheckSummary{1, 1, 1, 0, 0, 1, 2, 0}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := agyCheckClassify(agyCheckObservation{ID: "reconciliation", Summary: tc.summary}, tc.minimum)
			if r.OK != tc.ok {
				t.Fatalf("%+v", r)
			}
			if tc.ok && !strings.Contains(r.Observed, "repeats=0") {
				t.Fatalf("missing repeat count: %q", r.Observed)
			}
		})
	}
}

func TestAgyCheckReconciliationContradictingOutcome(t *testing.T) {
	for _, outcome := range []string{"error", "allowed", "unknown", "denied", "answered", "not exercisable", "covered if loaded", "unparsed transcript", "run timeout", "invalid hook events", "counts reconcile", ""} {
		t.Run(outcome, func(t *testing.T) {
			r := agyCheckClassify(agyCheckObservation{ID: "reconciliation", Outcome: outcome,
				Summary: agyCheckSummary{30, 30, 15, 15, 15, 15, 2, 0}}, agyCheckDefaultCalls)
			wantOK := outcome == "" || outcome == "counts reconcile"
			if r.OK != wantOK {
				t.Errorf("outcome %q: %+v", outcome, r)
			}
			wantObserved := "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0"
			if !wantOK {
				wantObserved += "; " + outcome
			}
			if r.Observed != wantObserved {
				t.Errorf("outcome %q replaced by %q", outcome, r.Observed)
			}
		})
	}
}

func TestAgyCheckReconciliationMissingDuplicate(t *testing.T) {
	for _, reason := range []string{"missing row", "duplicate row"} {
		t.Run(reason, func(t *testing.T) {
			pin, observations := agyCheckFixtures()
			for i, o := range observations {
				if o.ID == "reconciliation" {
					if reason == "missing row" {
						observations = append(observations[:i], observations[i+1:]...)
					} else {
						observations = append(observations, o)
					}
					break
				}
			}
			counts := "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0"
			if reason == "missing row" {
				counts = "steps=0 hooks=0 allows=0 denies=0 errors=0 files=0 repeats=0"
			}
			result := agyCheckEvaluate(pin, pin, observations, agyCheckDefaultCalls, "")
			for _, row := range result.Rows {
				if row.ID == "reconciliation" {
					if row.OK || row.Observed != counts+"; "+reason {
						t.Fatalf("lost counts or refusal: %+v", row)
					}
					return
				}
			}
			t.Fatal("reconciliation row missing from report")
		})
	}
}

func TestAgyCheckUnknownExpectation(t *testing.T) {
	for _, outcome := range []string{"", "unknown", "error", "allowed", "denied", "answered", "covered if loaded", "counts reconcile", "not exercisable"} {
		t.Run(outcome, func(t *testing.T) {
			r := agyCheckClassifyRow(agyCheckRow{Expected: "unknown"}, agyCheckObservation{Outcome: outcome}, agyCheckDefaultCalls)
			wantObserved := "unknown expectation: unknown"
			if outcome == "not exercisable" {
				wantObserved = outcome
			}
			if r.OK || r.Observed != wantObserved {
				t.Fatalf("outcome %q: %+v", outcome, r)
			}
		})
	}
}

func agyCheckAssertInvalid(t *testing.T, before, after agyCheckPin) {
	t.Helper()
	_, fixtures := agyCheckFixtures()
	r := agyCheckEvaluate(before, after, fixtures, agyCheckDefaultCalls, "")
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil {
		t.Fatal(err)
	}
	if r.InvalidReason == "" || len(r.Rows) != 0 || agyCheckExitCode(r) != 1 || out.String() != "RESULT INVALID: "+r.InvalidReason+"\n" {
		t.Fatalf("invalid result leaked rows/counts or succeeded: %+v; %q", r, out.String())
	}
}

func TestAgyCheckVersionPin(t *testing.T) {
	pin, _ := agyCheckFixtures()
	for _, version := range []string{"1.4.8", "", "bad", "1.4.7\n"} {
		t.Run(version, func(t *testing.T) {
			other := pin
			other.Version = version
			agyCheckAssertInvalid(t, pin, other)
			agyCheckAssertInvalid(t, other, pin)
		})
	}
}

func TestAgyCheckHashPin(t *testing.T) {
	pin, _ := agyCheckFixtures()
	for _, hash := range []string{strings.Repeat("b", 64), "", strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		t.Run(hash, func(t *testing.T) {
			other := pin
			other.ExecutableSHA256 = hash
			agyCheckAssertInvalid(t, pin, other)
			agyCheckAssertInvalid(t, other, pin)
		})
	}
}

func TestAgyCheckMalformedPins(t *testing.T) {
	pin, _ := agyCheckFixtures()
	for _, tc := range []struct{ name, version, hash string }{
		{"empty version", "", pin.ExecutableSHA256},
		{"bad version", "bad", pin.ExecutableSHA256},
		{"empty hash", pin.Version, ""},
		{"short hash", pin.Version, strings.Repeat("a", 63)},
		{"nonhex hash", pin.Version, strings.Repeat("g", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			malformed := agyCheckPin{tc.version, tc.hash}
			agyCheckAssertInvalid(t, malformed, malformed)
		})
	}
}

func TestAgyCheckInvalidResult(t *testing.T) {
	before, _ := agyCheckFixtures()
	after := before
	after.Version = "1.4.8"
	agyCheckAssertInvalid(t, before, after)
}

func TestAgyCheckMissingAndDuplicateRows(t *testing.T) {
	pin, fixtures := agyCheckFixtures()
	for _, tc := range []struct {
		name string
		rows []agyCheckObservation
	}{{"missing", fixtures[1:]}, {"duplicate", append(append([]agyCheckObservation{}, fixtures...), fixtures[0])}, {"empty", nil},
		{"missing reconciliation", fixtures[:len(fixtures)-2]},
		{"duplicate reconciliation", append(append([]agyCheckObservation{}, fixtures...), fixtures[len(fixtures)-2])},
		{"unknown", append(append([]agyCheckObservation{}, fixtures...), agyCheckObservation{ID: "unknown"})}} {
		t.Run(tc.name, func(t *testing.T) {
			r := agyCheckEvaluate(pin, pin, tc.rows, agyCheckDefaultCalls, "")
			if tc.name == "missing" && (len(r.Rows) == 0 || r.Rows[0].ID != fixtures[0].ID || r.Rows[0].Observed != "missing row" || r.Rows[0].OK) {
				t.Fatalf("missing row not identified: %+v", r)
			}
			for _, row := range r.Rows {
				if row.ID == "reconciliation" && !strings.HasPrefix(row.Observed, "steps=") {
					t.Fatalf("accounting counts hidden: %+v", row)
				}
			}
			var out strings.Builder
			if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 1 || strings.Contains(out.String(), agyCheckSuccessLine) || !strings.Contains(out.String(), "differs") {
				t.Fatalf("incomplete run passed: %+v; %s; %v", r, out.String(), err)
			}
		})
	}
}

func TestAgyCheckFinalLine(t *testing.T) {
	pin, fixtures := agyCheckFixtures()
	const final = "All rows match the known expectations. This is not a safety guarantee and not a trust decision; the maintainer decides.\n"
	for _, differs := range []bool{false, true} {
		fixtures[0].Outcome = "denied"
		if differs {
			fixtures[0].Outcome = "allowed"
		}
		r := agyCheckEvaluate(pin, pin, fixtures, agyCheckDefaultCalls, "")
		var out strings.Builder
		if err := agyCheckRender(&out, r); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(out.String(), final) != !differs || agyCheckSuccessLine+"\n" != final || (agyCheckExitCode(r) == 0) != !differs || strings.Count(out.String(), "\n") != 33-agyCheckExitCode(r) {
			t.Fatalf("unexpected report/exit: %+v; %s", r, out.String())
		}
	}
}

func TestAgyCheckRenderControls(t *testing.T) {
	for _, column := range []string{"ID", "Observed"} {
		t.Run(column, func(t *testing.T) {
			pin, fixtures := agyCheckFixtures()
			const controls = "\t\n\x00\x1b\u0085"
			if column == "ID" {
				fixtures = append(fixtures, agyCheckObservation{ID: "unknown" + controls})
			} else {
				fixtures[0].Outcome = "denied" + controls
			}
			r := agyCheckEvaluate(pin, pin, fixtures, agyCheckDefaultCalls, "")
			var out strings.Builder
			if err := agyCheckRender(&out, r); err != nil {
				t.Fatal(err)
			}
			if strings.Count(out.String(), "\n") != len(r.Rows)+2 || strings.ContainsFunc(out.String(), func(r rune) bool { return r != '\n' && !unicode.IsPrint(r) }) || !strings.Contains(out.String(), "?????") || strings.Contains(out.String(), agyCheckSuccessLine) {
				t.Fatalf("unsafe %s column: %q", column, out.String())
			}
		})
	}
}

func TestAgyCheckZeroMinimum(t *testing.T) {
	pin, fixtures := agyCheckFixtures()
	r := agyCheckEvaluate(pin, pin, fixtures, 0, "")
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil || r.UsageError != "minimum calls must be positive" || agyCheckExitCode(r) != 2 || !strings.HasPrefix(out.String(), "USAGE:") {
		t.Fatalf("zero minimum: %+v; %q; %v", r, out.String(), err)
	}
}

func TestAgyCheckUsageAndWriter(t *testing.T) {
	pin, fixtures := agyCheckFixtures()
	for _, r := range []agyCheckResult{agyCheckEvaluate(pin, pin, fixtures, agyCheckDefaultCalls, "bad input"), agyCheckEvaluate(pin, pin, fixtures, -1, "")} {
		var out strings.Builder
		if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 2 || !strings.HasPrefix(out.String(), "USAGE:") || strings.Contains(out.String(), agyCheckSuccessLine) {
			t.Fatalf("usage: %+v; %v", r, err)
		}
	}
	for _, failure := range []error{io.ErrClosedPipe, nil} {
		if err := agyCheckRender(agyCheckBadWriter{failure}, agyCheckEvaluate(pin, pin, fixtures, agyCheckDefaultCalls, "")); err == nil || (failure != nil && !errors.Is(err, failure)) {
			t.Fatalf("writer error lost: %v", err)
		}
	}
}

type agyCheckBadWriter struct{ err error }

func (w agyCheckBadWriter) Write([]byte) (int, error) { return 0, w.err }
