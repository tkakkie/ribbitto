package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// Independent fixtures pin both the row set and the known outcomes.
const agyCheckFixtureRows = `hook-decision=denied hook-exit2=denied hook-exit1=denied hook-missing=denied hook-timeout=denied hook-unparseable=denied hook-silent=allowed
discovery-agent=covered_if_loaded discovery-agents=covered_if_loaded discovery-gemini-config=covered_if_loaded discovery-gemini=covered_if_loaded discovery-antigravity=covered_if_loaded discovery-root=covered_if_loaded
hook-precedence=denied command-on=denied command-off=denied mcp-on=denied mcp-off=denied read-url=not_exercisable execute-url=not_exercisable
task-on=allowed task-off=allowed list-resources=answered read-resource=answered outside-write=allowed outside-read=allowed reconciliation=counts_reconcile`

func agyCheckFixtures() (agyCheckPin, []agyCheckObservation) {
	var observations []agyCheckObservation
	for _, pair := range strings.Fields(agyCheckFixtureRows) {
		id, outcome, _ := strings.Cut(pair, "=")
		observations = append(observations, agyCheckObservation{ID: id, Outcome: strings.ReplaceAll(outcome, "_", " "),
			Loaded: true, Ran: true, Summary: agyCheckSummary{30, 30, 15, 15, 15, 15, 2}})
	}
	return agyCheckPin{"1.4.7", strings.Repeat("a", 64)}, observations
}

func TestAgyCheckExpectedTable(t *testing.T) {
	_, fixtures := agyCheckFixtures()
	if len(agyCheckExpected) != 27 || len(fixtures) != len(agyCheckExpected) || agyCheckDefaultCalls != 30 {
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
		case "allowed", "denied", "answered", "not exercisable", "covered if loaded", "counts reconcile":
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
			r := agyCheckClassify(agyCheckObservation{ID: "hook-silent", Outcome: outcome}, 0)
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
			if r := agyCheckClassify(o, 0); !r.OK {
				t.Fatalf("fixture differs: %+v", r)
			}
			for _, outcome := range []string{"allowed", "denied", "answered", "not exercisable", "", "unknown"} {
				if (o.Outcome == "covered if loaded" || o.Outcome == "counts reconcile") && outcome != "not exercisable" {
					continue
				}
				changed := o
				changed.Outcome = outcome
				if r := agyCheckClassify(changed, 0); r.OK != (outcome == o.Outcome) {
					t.Fatalf("outcome %q: %+v", outcome, r)
				}
			}
		})
	}
	if agyCheckClassify(agyCheckObservation{ID: "unknown"}, 0).OK ||
		agyCheckClassifyRow(agyCheckRow{Expected: "unknown"}, agyCheckObservation{Outcome: "unknown"}, 0).OK {
		t.Fatal("unknown row/expectation matched")
	}
}

func TestAgyCheckDiscoveryRefusedNames(t *testing.T) {
	for _, name := range append(append([]string{}, implementRefusedNames...), "ordinary") {
		t.Run(name, func(t *testing.T) {
			row := agyCheckRow{Expected: "covered if loaded", Location: "nested/" + strings.ToUpper(name) + "/custom"}
			for _, facts := range []struct{ loaded, ran, ok bool }{{true, true, name != "ordinary"}, {false, false, true}, {true, false, false}, {false, true, false}} {
				r := agyCheckClassifyRow(row, agyCheckObservation{Loaded: facts.loaded, Ran: facts.ran}, 0)
				if r.OK != facts.ok {
					t.Fatalf("%+v: %+v", facts, r)
				}
			}
		})
	}
}

func TestAgyCheckReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary agyCheckSummary
		minimum int
		ok      bool
	}{
		{"default", agyCheckSummary{30, 30, 15, 15, 15, 15, 2}, 0, true},
		{"odd", agyCheckSummary{31, 31, 16, 15, 15, 16, 2}, 31, true},
		{"override", agyCheckSummary{40, 40, 20, 20, 20, 20, 2}, 40, true},
		{"too few", agyCheckSummary{28, 28, 14, 14, 14, 14, 2}, 0, false},
		{"override too few", agyCheckSummary{30, 30, 15, 15, 15, 15, 2}, 40, false},
		{"parallel", agyCheckSummary{30, 30, 15, 15, 15, 15, 1}, 0, false},
		{"steps", agyCheckSummary{31, 30, 15, 15, 15, 15, 2}, 0, false},
		{"hooks", agyCheckSummary{30, 31, 15, 15, 15, 15, 2}, 0, false},
		{"errors", agyCheckSummary{30, 30, 15, 15, 14, 15, 2}, 0, false},
		{"files", agyCheckSummary{30, 30, 15, 15, 15, 14, 2}, 0, false},
		{"total", agyCheckSummary{30, 30, 14, 15, 15, 14, 2}, 0, false},
		{"alternation", agyCheckSummary{30, 30, 17, 13, 13, 17, 2}, 0, false},
		{"negative", agyCheckSummary{30, 30, -1, 31, 31, -1, 2}, 0, false},
		{"no denies", agyCheckSummary{1, 1, 1, 0, 0, 1, 2}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := agyCheckClassify(agyCheckObservation{ID: "reconciliation", Summary: tc.summary}, tc.minimum); r.OK != tc.ok {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func agyCheckAssertInvalid(t *testing.T, before, after agyCheckPin) {
	t.Helper()
	_, fixtures := agyCheckFixtures()
	r := agyCheckEvaluate(before, after, fixtures, 0, "")
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
		{"unknown", append(append([]agyCheckObservation{}, fixtures...), agyCheckObservation{ID: "unknown"})}} {
		t.Run(tc.name, func(t *testing.T) {
			r := agyCheckEvaluate(pin, pin, tc.rows, 0, "")
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
		r := agyCheckEvaluate(pin, pin, fixtures, 0, "")
		var out strings.Builder
		if err := agyCheckRender(&out, r); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(out.String(), final) != !differs || agyCheckSuccessLine+"\n" != final || (agyCheckExitCode(r) == 0) != !differs || strings.Count(out.String(), "\n") != 30-agyCheckExitCode(r) {
			t.Fatalf("unexpected report/exit: %+v; %s", r, out.String())
		}
	}
}

func TestAgyCheckUsageAndWriter(t *testing.T) {
	pin, fixtures := agyCheckFixtures()
	for _, r := range []agyCheckResult{agyCheckEvaluate(pin, pin, fixtures, 0, "bad input"), agyCheckEvaluate(pin, pin, fixtures, -1, "")} {
		var out strings.Builder
		if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 2 || !strings.HasPrefix(out.String(), "USAGE:") || strings.Contains(out.String(), agyCheckSuccessLine) {
			t.Fatalf("usage: %+v; %v", r, err)
		}
	}
	for _, failure := range []error{io.ErrClosedPipe, nil} {
		if err := agyCheckRender(agyCheckBadWriter{failure}, agyCheckEvaluate(pin, pin, fixtures, 0, "")); err == nil || (failure != nil && !errors.Is(err, failure)) {
			t.Fatalf("writer error lost: %v", err)
		}
	}
}

type agyCheckBadWriter struct{ err error }

func (w agyCheckBadWriter) Write([]byte) (int, error) { return 0, w.err }
