package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// agyCheckObserveFixtures use the real stream-json envelope. Explicit
// JSONL makes each row's evidence reviewable without a runner or generated files.
// Same-name scenarios follow B1b's generator at call time.
func agyCheckObserveFixtures(t *testing.T) map[string]agyCheckObserveInput {
	t.Helper()
	transcripts := map[string]string{
		"hook-decision": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-exit2": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-exit1": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-missing": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-timeout": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-unparseable": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-silent": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-agent": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-agents": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-gemini-config": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-gemini": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-antigravity": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"discovery-root": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"hook-precedence": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"command-on": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"run_command"}}
{"event":"result","result":{}}
`,
		"command-off": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"run_command"}}
{"event":"result","result":{}}
`,
		"mcp-on": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"call_mcp_tool"}}
{"event":"result","result":{}}
`,
		"mcp-off": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"call_mcp_tool"}}
{"event":"result","result":{}}
`,
		"read-url": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"agent_response"}}
{"event":"result","result":{}}
`,
		"execute-url": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"agent_response"}}
{"event":"result","result":{}}
`,
		"task-on": `{"event":"step_update","step_update":{"step_index":0,"state":"ERROR","step_type":"tool","tool_name":"manage_task"}}
{"event":"result","result":{}}
`,
		"task-off": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"manage_task"}}
{"event":"result","result":{}}
`,
		"list-resources": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"list_resources"}}
{"event":"result","result":{}}
`,
		"read-resource": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"read_resource"}}
{"event":"result","result":{}}
`,
		"outside-write": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
		"outside-read": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"view_file"}}
{"event":"result","result":{}}
`,
		"reconciliation": `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":1,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":3,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":4,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":5,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":6,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":7,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":8,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":9,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":10,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":11,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":12,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":13,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":14,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":15,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":16,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":17,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":18,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":19,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":20,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":21,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":22,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":23,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":24,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":25,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":26,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":27,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":28,"state":"DONE","step_type":"tool","tool_name":"write_file"}}
{"event":"step_update","step_update":{"step_index":29,"state":"ERROR","step_type":"tool","tool_name":"write_file"}}
{"event":"result","result":{}}
`,
	}
	for _, fixture := range agyCheckObserveSameName(t) {
		transcripts[fixture.ID] = transcripts["hook-precedence"]
	}
	inputs := make(map[string]agyCheckObserveInput)
	for id, transcript := range transcripts {
		inputs[id] = agyCheckObserveInput{Transcript: []byte(agyCheckTestInit + transcript)}
	}
	for _, row := range agyCheckExpected {
		in := inputs[row.ID]
		if row.Group == "discovery" {
			in.RanLocations = []string{row.Location}
		}
		if row.Group == "reconciliation" {
			for i := range 30 {
				reply := "allow"
				if i%2 == 1 {
					reply = "deny"
				}
				in.Replies = append(in.Replies, reply)
			}
			for i := range 15 {
				in.CreatedFiles = append(in.CreatedFiles, fmt.Sprintf("created-%02d", i))
			}
		}
		inputs[row.ID] = in
	}
	parallel := inputs["reconciliation"]
	parallel.Replies = append([]string(nil), parallel.Replies...)
	parallel.CreatedFiles = append([]string(nil), parallel.CreatedFiles...)
	inputs["parallel-batches"] = parallel
	return inputs
}

func agyCheckObserveSameName(t *testing.T) []agyCheckSameNameConfig {
	t.Helper()
	// B1b only builds bytes and validates paths; these paths need not exist.
	fixtures, err := agyCheckConfigSameName("/trusted/argonreview", "/check/isolation/state")
	if err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func agyCheckObserveFindRow(t *testing.T, id string) agyCheckRow {
	t.Helper()
	for _, row := range agyCheckExpected {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing table row %s", id)
	return agyCheckRow{}
}

// TestAgyCheckObserveSameNameFixtures cross-checks generator, table, tool map
// and observe fixtures without duplicating B1b's JSON contract tests.
func TestAgyCheckObserveSameNameFixtures(t *testing.T) {
	fixtures := agyCheckObserveSameName(t)
	want := make(map[string]string)
	for _, row := range agyCheckExpected {
		if row.Group == "workspace" && row.Location != "" {
			want[row.ID] = row.Location
		}
	}
	if len(fixtures) != len(want) {
		t.Fatalf("same-name fixture/table set differs: %d fixtures, %d rows", len(fixtures), len(want))
	}
	inputs := agyCheckObserveFixtures(t)
	seen := make(map[string]bool)
	for _, fixture := range fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			if location, ok := want[fixture.ID]; !ok || location != fixture.Location || seen[fixture.ID] {
				t.Fatalf("same-name ID/location set drift: %+v", fixture)
			}
			seen[fixture.ID] = true
			name := []byte(`"ribbitto-implementer-guard"`)
			if bytes.Equal(fixture.Home, fixture.Workspace) || !bytes.Contains(fixture.Home, name) || !bytes.Contains(fixture.Workspace, name) {
				t.Fatal("same-name home/workspace bytes must differ and name the same hook")
			}
			if tool, ok := agyCheckTranscriptTools[fixture.ID]; !ok || tool != "write_file" {
				t.Fatal("same-name tool mapping missing or changed")
			}
			in, ok := inputs[fixture.ID]
			if !ok || !bytes.Equal(in.Transcript, inputs["hook-precedence"].Transcript) {
				t.Fatal("same-name observe fixture missing or differs from precedence")
			}
		})
	}
}

// TestAgyCheckObserveSameNameDispatch requires workspace outcome semantics even
// though these rows have locations, unlike the original precedence row.
func TestAgyCheckObserveSameNameDispatch(t *testing.T) {
	step := agyCheckTestStep(1, "write_file", "ERROR")
	const result = agyCheckTestResult
	for _, fixture := range agyCheckObserveSameName(t) {
		row := agyCheckObserveFindRow(t, fixture.ID)
		for _, tc := range []struct {
			name, transcript, outcome string
			ok                        bool
		}{
			{"error", step + result, "denied", true},
			{"done", strings.Replace(step, "ERROR", "DONE", 1) + result, "allowed", false},
			{"no step", result, "no step", false},
			{"two steps", step + agyCheckTestStep(2, "write_file", "ERROR") + result, "multiple steps", false},
			{"unparsed", "UNTRUSTED_TRANSCRIPT_MARKER\n", "unparsed transcript", false},
		} {
			t.Run(fixture.ID+"/"+tc.name, func(t *testing.T) {
				got := agyCheckObserveRow(row, agyCheckObserveInput{Transcript: []byte(agyCheckTestInit + tc.transcript)})
				classified := agyCheckClassify(got, agyCheckDefaultCalls)
				if got != (agyCheckObservation{ID: fixture.ID, Outcome: tc.outcome}) || classified.OK != tc.ok || classified.Observed != tc.outcome {
					t.Fatalf("same-name workspace observation=%+v classified=%+v want %s OK=%t", got, classified, tc.outcome, tc.ok)
				}
			})
		}
	}
}

// TestAgyCheckObserveSameNameIgnoresRecords pins the adapter boundary: B3 must
// check BOTH hook claims in claim-number order, final deny and absent side effect
// before a row may pass. That record cross-check belongs to B3 here.
func TestAgyCheckObserveSameNameIgnoresRecords(t *testing.T) {
	inputs := agyCheckObserveFixtures(t)
	for _, fixture := range agyCheckObserveSameName(t) {
		row := agyCheckObserveFindRow(t, fixture.ID)
		in := inputs[fixture.ID]
		// Loaded/Ran are discovery facts; same-name rows judge the tool outcome.
		want := agyCheckObservation{ID: fixture.ID, Outcome: "denied"}
		for _, tc := range []struct {
			name         string
			replies, ran []string
		}{
			{"neither", nil, nil},
			{"replies only", []string{"allow", "deny"}, nil},
			{"ran only", nil, []string{fixture.Location}},
			{"both", []string{"deny", "allow"}, []string{fixture.Location}},
			{"unknown records", []string{"UNTRUSTED_TRANSCRIPT_MARKER"}, []string{"elsewhere"}},
		} {
			t.Run(fixture.ID+"/"+tc.name, func(t *testing.T) {
				in.Replies, in.RanLocations = tc.replies, tc.ran
				if got := agyCheckObserveRow(row, in); got != want || !agyCheckClassify(got, agyCheckDefaultCalls).OK {
					t.Fatalf("same-name rows must ignore Replies and RanLocations: %+v want %+v", got, want)
				}
			})
		}
	}
}

// agyCheckObserveEvaluate gives every integration assertion the same valid pins.
func agyCheckObserveEvaluate(t *testing.T, inputs map[string]agyCheckObserveInput) (agyCheckResult, string) {
	t.Helper()
	pin := agyCheckPin{"1.4.7", strings.Repeat("a", 64)}
	r := agyCheckEvaluate(pin, pin, agyCheckObserveAll(inputs), 30, "")
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil {
		t.Fatal(err)
	}
	return r, out.String()
}

// agyCheckObserveAssertDifference verifies a malformed row cannot retain success
// or leak planted transcript content through the core's observed column.
func agyCheckObserveAssertDifference(t *testing.T, inputs map[string]agyCheckObserveInput, id, observed string) {
	t.Helper()
	r, out := agyCheckObserveEvaluate(t, inputs)
	if agyCheckExitCode(r) != 1 || strings.Contains(out, agyCheckSuccessLine) {
		t.Fatalf("differing run succeeded: %+v", r)
	}
	found := false
	for _, row := range r.Rows {
		if strings.Contains(row.Observed, "UNTRUSTED_TRANSCRIPT_MARKER") {
			t.Fatalf("observed text echoed transcript: %q", row.Observed)
		}
		if row.ID == id {
			found = true
			if id == "reconciliation" {
				if strings.HasPrefix(observed, "steps=") {
					observed += "; " + agyCheckBatchesUnobservable
				} else {
					if observed != "missing input" && agyCheckObserveRow(agyCheckObserveFindRow(t, id), inputs[id]).Outcome != observed {
						t.Fatal("accounting refusal reason lost")
					}
					observed = "steps=0 hooks=0 allows=0 denies=0 errors=0 files=0 repeats=0; " + observed
				}
			}
			if row.OK || row.Observed != observed {
				t.Fatalf("observed=%q OK=%t; want %q false", row.Observed, row.OK, observed)
			}
		} else if row.ID == "reconciliation" {
			if row.OK || row.Observed != "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0; "+agyCheckBatchesUnobservable {
				t.Fatalf("batches invented: %+v", row)
			}
		} else if row.ID == "parallel-batches" {
			if row.OK || row.Observed != agyCheckBatchesUnobservable {
				t.Fatalf("adapter invented overlap evidence: %+v", row)
			}
		} else if !row.OK {
			t.Fatalf("unrelated row differs: %+v", row)
		}
	}
	if !found {
		t.Fatalf("missing differing row %s", id)
	}
}

// TestAgyCheckObserveAll pins the assumed scenarios and deterministic order.
func TestAgyCheckObserveAll(t *testing.T) {
	inputs := agyCheckObserveFixtures(t)
	observations := agyCheckObserveAll(inputs)
	if len(agyCheckExpected) != 30 {
		t.Fatal("expected row count changed")
	}
	if len(inputs) != len(agyCheckExpected) || len(observations) != len(agyCheckExpected) {
		t.Fatal("fixture/table size drift")
	}
	for i, row := range agyCheckExpected {
		if observations[i].ID != row.ID {
			t.Fatalf("observation order at %d: %s want %s", i, observations[i].ID, row.ID)
		}
	}
	r, out := agyCheckObserveEvaluate(t, inputs)
	if len(r.Rows) != len(agyCheckExpected) || strings.Count(out, "\n") != len(agyCheckExpected)+2 ||
		!strings.Contains(out, fmt.Sprintf("Rows: %d; differing: 2\n", len(agyCheckExpected))) {
		t.Fatalf("report row count drift: %q", out)
	}
	for _, row := range r.Rows {
		if row.ID == "reconciliation" && (row.OK || row.Observed != "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0; "+agyCheckBatchesUnobservable) {
			t.Fatalf("batches invented: %+v", row)
		}
		if !row.OK && row.ID != "reconciliation" && row.ID != "parallel-batches" {
			t.Fatalf("assumed fixture differs: %+v", row)
		}
	}
	if agyCheckExitCode(r) != 1 || strings.Contains(out, agyCheckSuccessLine) {
		t.Fatalf("passing fixtures lost success: %q", out)
	}
	for _, row := range agyCheckExpected {
		t.Run("missing/"+row.ID, func(t *testing.T) {
			inputs := agyCheckObserveFixtures(t)
			delete(inputs, row.ID)
			agyCheckObserveAssertDifference(t, inputs, row.ID, "missing input")
		})
	}
}

// TestAgyCheckObserveUnknownRows keeps unexpected input visible after table rows.
func TestAgyCheckObserveUnknownRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"forward", []string{"Hook-Decision", "a-extra", "extra\n\t\x1b", "z-extra"}},
		{"reverse", []string{"z-extra", "extra\n\t\x1b", "a-extra", "Hook-Decision"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := agyCheckObserveFixtures(t)
			want := agyCheckObserveAll(inputs)
			unknown := []string{"Hook-Decision", "a-extra", "extra\n\t\x1b", "z-extra"}
			for _, id := range unknown {
				want = append(want, agyCheckObservation{ID: id, Outcome: "unknown row"})
			}
			for _, id := range tc.keys {
				inputs[id] = agyCheckObserveInput{}
			}
			if got := agyCheckObserveAll(inputs); !slices.Equal(got, want) {
				t.Fatalf("observation list=%+v want %+v", got, want)
			}
			r, out := agyCheckObserveEvaluate(t, inputs)
			if agyCheckExitCode(r) != 1 || strings.Contains(out, agyCheckSuccessLine) || len(r.Rows) != len(want) {
				t.Fatalf("unknown rows lost differing result: %+v", r)
			}
			for i, row := range r.Rows {
				if i < len(agyCheckExpected) {
					if row.ID == "reconciliation" && (row.OK || row.Observed != "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0; "+agyCheckBatchesUnobservable) {
						t.Fatalf("batches invented: %+v", row)
					}
					if !row.OK && row.ID != "reconciliation" && row.ID != "parallel-batches" {
						t.Fatalf("unrelated row differs: %+v", row)
					}
				} else if row.ID != want[i].ID || row.OK || row.Observed != "unknown row" {
					t.Fatalf("unknown row=%+v want %+v differing", row, want[i])
				}
			}
			for _, id := range []string{"Hook-Decision", "a-extra", "extra???", "z-extra"} {
				if !strings.Contains(out, "\n"+id+" ") {
					t.Fatalf("printable unknown ID %q missing from report: %q", id, out)
				}
			}
			if strings.ContainsAny(out, "\t\x1b") || strings.Contains(out, "extra\n") {
				t.Fatalf("unknown ID escaped printable sanitisation: %q", out)
			}
		})
	}
}

// TestAgyCheckObserveDiscoverySteps requires evidence before judging discovery.
func TestAgyCheckObserveDiscoverySteps(t *testing.T) {
	for _, row := range agyCheckExpected {
		if row.Group != "discovery" {
			continue
		}
		for _, tc := range []struct {
			name         string
			step, loaded bool
		}{
			{"result only", false, false},
			{"loaded without step", false, true},
			{"step neither loaded nor ran", true, false},
			{"step loaded and ran", true, true},
		} {
			t.Run(row.ID+"/"+tc.name, func(t *testing.T) {
				inputs := agyCheckObserveFixtures(t)
				in := agyCheckObserveInput{Transcript: []byte(agyCheckTestInit)}
				if tc.loaded {
					in.RanLocations = []string{row.Location}
				}
				if tc.step {
					in.Transcript = append(in.Transcript, agyCheckTestStep(1, "write_file", "ERROR")...)
				}
				in.Transcript = append(in.Transcript, agyCheckTestResult...)
				inputs[row.ID] = in
				want := ""
				if !tc.step {
					want = "no step"
				} else if !tc.loaded {
					want = agyCheckLoadUnobservable
				}
				got := agyCheckObserveRow(row, in)
				if want != "" {
					agyCheckObserveAssertDifference(t, inputs, row.ID, want)
				} else if !agyCheckClassify(got, 30).OK {
					t.Fatalf("run record lost: %+v", got)
				}

			})
		}
	}
}

// TestAgyCheckObserveSingleChanges checks each single-call tool independently;
// URL rows pass only with no step, so either observed state differs there.
func TestAgyCheckObserveSingleChanges(t *testing.T) {
	count := 0
	for _, row := range agyCheckExpected {
		// Discovery and reconciliation now have tool mappings for scenario
		// generation; their adapter predicates have separate tests below.
		if row.Group == "discovery" || row.Group == "reconciliation" || row.Group == "parallel" {
			continue
		}
		tool, single := agyCheckTranscriptTools[row.ID]
		if !single {
			t.Fatalf("single-call row lost its mapping: %s", row.ID)
		}
		count++
		for _, change := range []string{"flip", "drop", "duplicate"} {
			t.Run(row.ID+"/"+change, func(t *testing.T) {
				inputs := agyCheckObserveFixtures(t)
				in := inputs[row.ID]
				_, rest, _ := strings.Cut(string(in.Transcript), "\n")
				line, _, _ := strings.Cut(rest, "\n")
				url := row.ID == "read-url" || row.ID == "execute-url"
				if url {
					line = strings.TrimSuffix(agyCheckTestStep(0, tool, "ERROR"), "\n")
				}
				want := "multiple steps"
				switch change {
				case "flip":
					if strings.Contains(line, "ERROR") {
						line = strings.Replace(line, "ERROR", "DONE", 1)
						want = "allowed"
						if row.Expected == "answered" {
							want = "answered"
						}
					} else {
						line = strings.Replace(line, "DONE", "ERROR", 1)
						want = "denied"
					}
					in.Transcript = []byte(agyCheckTestInit + line + "\n" + agyCheckTestResult)
				case "drop":
					in.Transcript = []byte(agyCheckTestInit + agyCheckTestResult)
					want = "no step"
				case "duplicate":
					in.Transcript = []byte(agyCheckTestInit + line + "\n" + strings.Replace(line, `"step_index":0`, `"step_index":1`, 1) + "\n" + agyCheckTestResult)
				}
				inputs[row.ID] = in
				if url && change == "drop" {
					got := agyCheckObserveRow(row, in)
					if got.Outcome != "not exercisable" || !agyCheckClassify(got, 30).OK {
						t.Fatalf("absent URL step: %+v", got)
					}
					return
				}
				if row.ID == "task-on" && change == "flip" {
					got := agyCheckClassify(agyCheckObserveRow(row, in), 30)
					if !got.OK || got.Observed != want {
						t.Fatalf("task-on flip must preserve allowed outcome: %+v", got)
					}
					return
				}
				agyCheckObserveAssertDifference(t, inputs, row.ID, want)
			})
		}
	}
	if count != 22 {
		t.Fatal("single-call adapter test domain changed")
	}
}

// TestAgyCheckObserveParseFailures exercises every parser rejection through
// every dispatch group; discovery must never turn parse failure into unloaded.
func TestAgyCheckObserveParseFailures(t *testing.T) {
	for _, tc := range agyCheckTranscriptFailures() {
		for _, id := range []string{"hook-decision", "hook-precedence", "command-on", "task-on", "discovery-agent", "reconciliation"} {
			t.Run(tc.name+"/"+id, func(t *testing.T) {
				inputs := agyCheckObserveFixtures(t)
				in := inputs[id]
				in.Transcript = []byte(tc.data)
				inputs[id] = in
				var row agyCheckRow
				for _, candidate := range agyCheckExpected {
					if candidate.ID == id {
						row = candidate
					}
				}
				got := agyCheckObserveRow(row, in)
				if got != (agyCheckObservation{ID: id, Outcome: "unparsed transcript"}) {
					t.Fatalf("parse error retained facts or echoed content: %+v", got)
				}
				agyCheckObserveAssertDifference(t, inputs, id, "unparsed transcript")
			})
		}
	}
}

// TestAgyCheckObserveUnknownGroup proves a future group cannot pass by default.
func TestAgyCheckObserveUnknownGroup(t *testing.T) {
	row := agyCheckExpected[0]
	row.Group = "UNTRUSTED_TRANSCRIPT_MARKER"
	got := agyCheckObserveRow(row, agyCheckObserveFixtures(t)[row.ID])
	if got != (agyCheckObservation{ID: row.ID, Outcome: "unparsed transcript"}) {
		t.Fatalf("unknown group: %+v", got)
	}
	inputs := agyCheckObserveFixtures(t)
	observations := agyCheckObserveAll(inputs)
	observations[0] = got
	pin := agyCheckPin{"1.4.7", strings.Repeat("a", 64)}
	r := agyCheckEvaluate(pin, pin, observations, 30, "")
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 1 || r.Rows[0].OK || r.Rows[0].Observed != "unparsed transcript" || strings.Contains(out.String(), agyCheckSuccessLine) {
		t.Fatalf("unknown group passed: %+v %v", r, err)
	}
}

// TestAgyCheckObserveDiscovery requires an exact run-record location.
func TestAgyCheckObserveDiscovery(t *testing.T) {
	row := agyCheckExpected[7]
	for _, location := range []string{"", row.Location, row.Location + ".extra", "prefix/" + row.Location, strings.ToUpper(row.Location)} {
		got := agyCheckObserveDiscovery(row, []string{location})
		match := location == row.Location
		if got.Loaded != match || got.Ran != match || agyCheckClassify(got, 30).OK != match {
			t.Fatalf("run evidence: %+v", got)
		}
		if !match && got.Outcome != agyCheckLoadUnobservable {
			t.Fatal(got)
		}
	}
	synthetic := agyCheckRow{ID: "synthetic", Group: "discovery", Expected: "covered if loaded", Location: "ordinary/location"}
	if got := agyCheckClassifyRow(synthetic, agyCheckObserveDiscovery(synthetic, []string{synthetic.Location}), 30); got.OK || got.Observed != "loaded=true ran=true covered=false" {
		t.Fatal(got)
	}
}

// TestAgyCheckObserveSummary pins exact counts rather than only the core's OK.
func TestAgyCheckObserveSummary(t *testing.T) {
	in := agyCheckObserveFixtures(t)["reconciliation"]
	transcript, err := agyCheckTranscriptParse(in.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		replies []string
		repeats int
	}{
		{"allow first", in.Replies, 0},
		{"deny first", append(append([]string{}, in.Replies[1:]...), "allow"), 0},
		{"grouped", append(strings.Fields(strings.Repeat("allow ", 15)), strings.Fields(strings.Repeat("deny ", 15))...), 28},
		{"one repeat", []string{"allow", "allow", "deny"}, 1},
		{"three repeats", []string{"allow", "allow", "allow", "allow", "deny"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckObserveSummary(transcript.Steps, tc.replies, in.CreatedFiles)
			want := agyCheckSummary{30, len(tc.replies), 0, 0, 15, 15, 0, tc.repeats}
			for _, reply := range tc.replies {
				if reply == "allow" {
					want.Allows++
				} else {
					want.Denies++
				}
			}
			if err != nil || got != want {
				t.Fatalf("summary=%+v error=%v want %+v", got, err, want)
			}
		})
	}
	for _, tc := range []struct {
		name           string
		steps          []agyCheckTranscriptStep
		replies, files []string
	}{
		{"unknown state", []agyCheckTranscriptStep{{State: "UNTRUSTED_TRANSCRIPT_MARKER"}}, nil, nil},
		{"unknown reply", nil, []string{"UNTRUSTED_TRANSCRIPT_MARKER"}, nil},
		{"duplicate file", nil, nil, []string{"created-a", "created-a"}},
		{"unprefixed file", nil, nil, []string{"UNTRUSTED_TRANSCRIPT_MARKER"}},
		{"slash file", nil, nil, []string{"created-a/b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agyCheckObserveSummary(tc.steps, tc.replies, tc.files)
			if err == nil || got != (agyCheckSummary{}) || strings.Contains(err.Error(), "UNTRUSTED_TRANSCRIPT_MARKER") {
				t.Fatalf("summary failure retained facts or echoed content: %+v %v", got, err)
			}
		})
	}
}

// TestAgyCheckObserveReconciliationChanges changes one independent count or
// record rule at a time; matching totals alone must never prove alternation.
func TestAgyCheckObserveReconciliationChanges(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*agyCheckObserveInput)
	}{
		{"invocations", "steps=30 hooks=29 allows=15 denies=14 errors=15 files=15 repeats=0", func(in *agyCheckObserveInput) { in.Replies = in.Replies[:29] }},
		{"errors", "steps=30 hooks=30 allows=15 denies=15 errors=14 files=15 repeats=0", func(in *agyCheckObserveInput) {
			in.Transcript = []byte(strings.Replace(string(in.Transcript), "ERROR", "DONE", 1))
		}},
		{"files", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=14 repeats=0", func(in *agyCheckObserveInput) { in.CreatedFiles = in.CreatedFiles[:14] }},
		{"parallel unobservable", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0", func(in *agyCheckObserveInput) {}},
		{"below minimum", "steps=29 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=0", func(in *agyCheckObserveInput) {
			lines := strings.SplitAfter(string(in.Transcript), "\n")
			in.Transcript = []byte(strings.Join(append(lines[:29], lines[30:]...), ""))
		}},
		{"unknown reply", "unparsed transcript", func(in *agyCheckObserveInput) { in.Replies[0] = "UNTRUSTED_TRANSCRIPT_MARKER" }},
		{"non alternating", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 repeats=28", func(in *agyCheckObserveInput) {
			in.Replies = append(strings.Fields(strings.Repeat("allow ", 15)), strings.Fields(strings.Repeat("deny ", 15))...)
		}},
		{"duplicate file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[1] = in.CreatedFiles[0] }},
		{"unprefixed file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[0] = "UNTRUSTED_TRANSCRIPT_MARKER" }},
		{"slash file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[0] = "created-/UNTRUSTED_TRANSCRIPT_MARKER" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := agyCheckObserveFixtures(t)
			in := inputs["reconciliation"]
			tc.change(&in)
			inputs["reconciliation"] = in
			got := agyCheckObserveRow(agyCheckObserveFindRow(t, "reconciliation"), in)
			if tc.want == "unparsed transcript" && got != (agyCheckObservation{ID: "reconciliation", Outcome: "unparsed transcript"}) {
				t.Fatalf("summary failure retained facts or echoed content: %+v", got)
			}
			if strings.HasPrefix(tc.want, "steps=") {
				summary := got.Summary
				actual := fmt.Sprintf("steps=%d hooks=%d allows=%d denies=%d errors=%d files=%d repeats=%d", summary.ToolSteps, summary.HookInvocations, summary.Allows, summary.Denies, summary.Errors, summary.FilesCreated, summary.Repeats)
				if actual != tc.want {
					t.Fatalf("counts=%s want %s", actual, tc.want)
				}
				// Accounting always renders counts, including on refusal.
			}
			agyCheckObserveAssertDifference(t, inputs, "reconciliation", tc.want)
		})
	}
}

// TestAgyCheckBuiltinInfo uses implement-check's exact syntax while independently
// pinning the digest, bytes and wording. Information must never enter evaluate.
func TestAgyCheckBuiltinInfo(t *testing.T) {
	first := strings.Repeat("a", 64) + "  ./z/file\n"
	second := "LINK ./a/link\n"
	const invalid = "INFO builtin/ listing could not be hashed: invalid listing"
	for _, tc := range []struct {
		name, listing string
		valid         bool
	}{
		{"valid", first + second, true}, {"reordered", second + first, true},
		{"empty", "", false}, {"syntax", "UNTRUSTED_TRANSCRIPT_MARKER\n", false},
		{"newline", strings.TrimSuffix(first, "\n"), false}, {"duplicate", first + first, false},
		{"parent", "LINK ./../UNTRUSTED_TRANSCRIPT_MARKER\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := agyCheckBuiltinInfo([]byte(tc.listing))
			want := invalid
			if tc.valid {
				want = fmt.Sprintf("INFO builtin/ listing SHA-256 %x over %d bytes; information only, no trusted file is written; the maintainer decides", sha256.Sum256([]byte(tc.listing)), len(tc.listing))
			}
			if info != want || strings.Contains(info, "UNTRUSTED_TRANSCRIPT_MARKER") {
				t.Fatalf("builtin info=%q want %q", info, want)
			}
			beforeR, before := agyCheckObserveEvaluate(t, agyCheckObserveFixtures(t))
			_ = agyCheckBuiltinInfo([]byte(tc.listing))
			afterR, after := agyCheckObserveEvaluate(t, agyCheckObserveFixtures(t))
			if agyCheckExitCode(beforeR) != 1 || agyCheckExitCode(afterR) != 1 || before != after || strings.Contains(after, "INFO builtin/") {
				t.Fatal("builtin information altered exit code or report")
			}
		})
	}
}

// TestAgyCheckBuiltinInfoSize pins implement-check's inclusive listing limit.
func TestAgyCheckBuiltinInfoSize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		valid bool
	}{
		{"at cap", 1 << 20, true},
		{"over cap", (1 << 20) + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listing := []byte("LINK ./" + strings.Repeat("a", tc.size-len("LINK ./\n")) + "\n")
			if _, err := implementBuiltinList(listing); err != nil || len(listing) != tc.size {
				t.Fatalf("size fixture is not valid listing syntax: bytes=%d error=%v", len(listing), err)
			}
			want := "INFO builtin/ listing could not be hashed: invalid listing"
			if tc.valid {
				want = fmt.Sprintf("INFO builtin/ listing SHA-256 %x over %d bytes; information only, no trusted file is written; the maintainer decides", sha256.Sum256(listing), len(listing))
			}
			if got := agyCheckBuiltinInfo(listing); got != want {
				t.Fatalf("builtin info at %d bytes=%q want %q", tc.size, got, want)
			}
		})
	}
}
