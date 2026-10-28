package main

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// agyCheckObserveFixtures are ASSUMED, unverified against real agy. Explicit
// JSONL makes each row's evidence reviewable without a runner or generated files.
func agyCheckObserveFixtures() map[string]agyCheckObserveInput {
	transcripts := map[string]string{
		"hook-decision": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-exit2": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-exit1": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-missing": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-timeout": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-unparseable": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"hook-silent": `{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"result"}
`,
		"discovery-agent": `{"event":"hook_loaded","path":".agent/hooks.json"}
{"event":"result"}
`,
		"discovery-agents": `{"event":"hook_loaded","path":".agents/hooks.json"}
{"event":"result"}
`,
		"discovery-gemini-config": `{"event":"hook_loaded","path":".gemini/config/hooks.json"}
{"event":"result"}
`,
		"discovery-gemini": `{"event":"hook_loaded","path":".gemini/hooks.json"}
{"event":"result"}
`,
		"discovery-antigravity": `{"event":"hook_loaded","path":".antigravity/hooks.json"}
{"event":"result"}
`,
		"discovery-root": `{"event":"hook_loaded","path":"hooks.json"}
{"event":"result"}
`,
		"hook-precedence": `{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
		"command-on": `{"event":"tool_step","tool":"run_command","state":"ERROR"}
{"event":"result"}
`,
		"command-off": `{"event":"tool_step","tool":"run_command","state":"ERROR"}
{"event":"result"}
`,
		"mcp-on": `{"event":"tool_step","tool":"call_mcp_tool","state":"ERROR"}
{"event":"result"}
`,
		"mcp-off": `{"event":"tool_step","tool":"call_mcp_tool","state":"ERROR"}
{"event":"result"}
`,
		"read-url": `{"event":"message"}
{"event":"result"}
`,
		"execute-url": `{"event":"message"}
{"event":"result"}
`,
		"task-on": `{"event":"tool_step","tool":"manage_task","state":"DONE"}
{"event":"result"}
`,
		"task-off": `{"event":"tool_step","tool":"manage_task","state":"DONE"}
{"event":"result"}
`,
		"list-resources": `{"event":"tool_step","tool":"list_resources","state":"DONE"}
{"event":"result"}
`,
		"read-resource": `{"event":"tool_step","tool":"read_resource","state":"DONE"}
{"event":"result"}
`,
		"outside-write": `{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"result"}
`,
		"outside-read": `{"event":"tool_step","tool":"view_file","state":"DONE"}
{"event":"result"}
`,
		"reconciliation": `{"event":"tool_step","tool":"write_to_file","state":"DONE","batch":"first"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR","batch":"first"}
{"event":"tool_step","tool":"write_to_file","state":"DONE","batch":"second"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR","batch":"second"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"tool_step","tool":"write_to_file","state":"DONE"}
{"event":"tool_step","tool":"write_to_file","state":"ERROR"}
{"event":"result"}
`,
	}
	inputs := make(map[string]agyCheckObserveInput)
	for id, transcript := range transcripts {
		inputs[id] = agyCheckObserveInput{Transcript: []byte(transcript)}
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
	return inputs
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
			if row.OK || row.Observed != observed {
				t.Fatalf("observed=%q OK=%t; want %q false", row.Observed, row.OK, observed)
			}
		} else if !row.OK {
			t.Fatalf("unrelated row differs: %+v", row)
		}
	}
	if !found {
		t.Fatalf("missing differing row %s", id)
	}
}

// TestAgyCheckObserveAll pins all 27 assumed scenarios and deterministic order.
func TestAgyCheckObserveAll(t *testing.T) {
	inputs := agyCheckObserveFixtures()
	observations := agyCheckObserveAll(inputs)
	if len(inputs) != 27 || len(observations) != 27 {
		t.Fatal("fixture/table size drift")
	}
	for i, row := range agyCheckExpected {
		if observations[i].ID != row.ID {
			t.Fatalf("observation order at %d: %s want %s", i, observations[i].ID, row.ID)
		}
	}
	r, out := agyCheckObserveEvaluate(t, inputs)
	for _, row := range r.Rows {
		if !row.OK {
			t.Fatalf("assumed fixture differs: %+v", row)
		}
	}
	if agyCheckExitCode(r) != 0 || !strings.HasSuffix(out, agyCheckSuccessLine+"\n") {
		t.Fatalf("passing fixtures lost success: %q", out)
	}
	for _, row := range agyCheckExpected {
		t.Run("missing/"+row.ID, func(t *testing.T) {
			inputs := agyCheckObserveFixtures()
			delete(inputs, row.ID)
			agyCheckObserveAssertDifference(t, inputs, row.ID, "missing input")
		})
	}
}

// TestAgyCheckObserveSingleChanges checks each single-call tool independently;
// URL rows pass only with no step, so either observed state differs there.
func TestAgyCheckObserveSingleChanges(t *testing.T) {
	for _, row := range agyCheckExpected {
		tool, single := agyCheckTranscriptTools[row.ID]
		if !single {
			continue
		}
		for _, change := range []string{"flip", "drop", "duplicate"} {
			t.Run(row.ID+"/"+change, func(t *testing.T) {
				inputs := agyCheckObserveFixtures()
				in := inputs[row.ID]
				line, _, _ := strings.Cut(string(in.Transcript), "\n")
				url := row.ID == "read-url" || row.ID == "execute-url"
				if url {
					line = fmt.Sprintf(`{"event":"tool_step","tool":%q,"state":"ERROR"}`, tool)
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
					in.Transcript = []byte(line + "\n{\"event\":\"result\"}\n")
				case "drop":
					in.Transcript = []byte("{\"event\":\"result\"}\n")
					want = "no step"
				case "duplicate":
					in.Transcript = []byte(line + "\n" + line + "\n{\"event\":\"result\"}\n")
				}
				inputs[row.ID] = in
				if url && change == "drop" {
					got := agyCheckObserveRow(row, in)
					if got.Outcome != "not exercisable" || !agyCheckClassify(got, 30).OK {
						t.Fatalf("absent URL step: %+v", got)
					}
					return
				}
				agyCheckObserveAssertDifference(t, inputs, row.ID, want)
			})
		}
	}
}

// TestAgyCheckObserveParseFailures exercises every parser rejection through
// every dispatch group; discovery must never turn parse failure into unloaded.
func TestAgyCheckObserveParseFailures(t *testing.T) {
	for _, tc := range agyCheckTranscriptFailures() {
		for _, id := range []string{"hook-decision", "hook-precedence", "command-on", "task-on", "discovery-agent", "reconciliation"} {
			t.Run(tc.name+"/"+id, func(t *testing.T) {
				inputs := agyCheckObserveFixtures()
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
	got := agyCheckObserveRow(row, agyCheckObserveFixtures()[row.ID])
	if got != (agyCheckObservation{ID: row.ID, Outcome: "unparsed transcript"}) {
		t.Fatalf("unknown group: %+v", got)
	}
	inputs := agyCheckObserveFixtures()
	observations := agyCheckObserveAll(inputs)
	observations[0] = got
	pin := agyCheckPin{"1.4.7", strings.Repeat("a", 64)}
	r := agyCheckEvaluate(pin, pin, observations, 30, "")
	var out strings.Builder
	if err := agyCheckRender(&out, r); err != nil || agyCheckExitCode(r) != 1 || r.Rows[0].OK || r.Rows[0].Observed != "unparsed transcript" || strings.Contains(out.String(), agyCheckSuccessLine) {
		t.Fatalf("unknown group passed: %+v %v", r, err)
	}
}

// TestAgyCheckObserveDiscovery keeps loaded and ran independent, including
// near misses that must not acquire coverage by sharing part of a path.
func TestAgyCheckObserveDiscovery(t *testing.T) {
	row := agyCheckExpected[7]
	for _, tc := range []struct {
		name                    string
		loaded, ran             []string
		wantLoaded, wantRan, ok bool
	}{
		{"neither", nil, nil, false, false, true},
		{"both", []string{row.Location}, []string{row.Location}, true, true, true},
		{"loaded only", []string{row.Location}, nil, true, false, false},
		{"ran only", nil, []string{row.Location}, false, true, false},
		{"prefix", []string{row.Location + ".extra"}, nil, false, false, true},
		{"suffix", []string{"prefix/" + row.Location}, nil, false, false, true},
		{"case", []string{strings.ToUpper(row.Location)}, nil, false, false, true},
		{"ran near miss", nil, []string{row.Location + ".extra"}, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agyCheckObserveDiscovery(row, tc.loaded, tc.ran)
			classified := agyCheckClassify(got, 30)
			if got.ID != row.ID || got.Outcome != "" || got.Loaded != tc.wantLoaded || got.Ran != tc.wantRan || classified.OK != tc.ok {
				t.Fatalf("discovery=%+v classified=%+v", got, classified)
			}
		})
	}
	synthetic := agyCheckRow{ID: "synthetic", Group: "discovery", Expected: "covered if loaded", Location: "ordinary/location"}
	got := agyCheckObserveDiscovery(synthetic, []string{synthetic.Location}, []string{synthetic.Location})
	r := agyCheckClassifyRow(synthetic, got, 30)
	if r.OK || r.Observed != "loaded=true ran=true covered=false" {
		t.Fatalf("uncovered load accepted: %+v", r)
	}
}

// TestAgyCheckObserveUnlistedLoads pins exact membership, sorting and duplicate
// removal without allowing extra locations to become result rows.
func TestAgyCheckObserveUnlistedLoads(t *testing.T) {
	inputs := []string{"z/new", ".agent/hooks.json", "a/new", "z/new", "hooks.json", "Hooks.json", "a/new"}
	want := []string{"Hooks.json", "a/new", "z/new"}
	if got := agyCheckObserveUnlistedLoads(inputs); !slices.Equal(got, want) {
		t.Fatalf("extra loads=%v want %v", got, want)
	}
	if !slices.Equal(inputs, []string{"z/new", ".agent/hooks.json", "a/new", "z/new", "hooks.json", "Hooks.json", "a/new"}) {
		t.Fatal("input loads were mutated")
	}
	r, before := agyCheckObserveEvaluate(t, agyCheckObserveFixtures())
	_ = agyCheckObserveUnlistedLoads(inputs)
	afterR, after := agyCheckObserveEvaluate(t, agyCheckObserveFixtures())
	if agyCheckExitCode(r) != 0 || agyCheckExitCode(afterR) != 0 || before != after {
		t.Fatal("unlisted information altered result")
	}
}

// TestAgyCheckObserveSummary pins exact counts rather than only the core's OK.
func TestAgyCheckObserveSummary(t *testing.T) {
	in := agyCheckObserveFixtures()["reconciliation"]
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
			want := agyCheckSummary{30, len(tc.replies), 0, 0, 15, 15, 2, tc.repeats}
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
		{"disjoint batch", []agyCheckTranscriptStep{{State: "DONE", Batch: "a"}, {State: "DONE", Batch: "b"}, {State: "DONE", Batch: "a"}}, nil, nil},
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
		{"invocations", "steps=30 hooks=29 allows=15 denies=14 errors=15 files=15 batches=2 repeats=0", func(in *agyCheckObserveInput) { in.Replies = in.Replies[:29] }},
		{"errors", "steps=30 hooks=30 allows=15 denies=15 errors=14 files=15 batches=2 repeats=0", func(in *agyCheckObserveInput) {
			in.Transcript = []byte(strings.Replace(string(in.Transcript), "ERROR", "DONE", 1))
		}},
		{"files", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=14 batches=2 repeats=0", func(in *agyCheckObserveInput) { in.CreatedFiles = in.CreatedFiles[:14] }},
		{"one parallel", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 batches=1 repeats=0", func(in *agyCheckObserveInput) {
			in.Transcript = []byte(strings.ReplaceAll(string(in.Transcript), `,"batch":"second"`, ""))
		}},
		{"single batch step", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 batches=1 repeats=0", func(in *agyCheckObserveInput) {
			in.Transcript = []byte(strings.Replace(string(in.Transcript), `,"batch":"second"`, "", 1))
		}},
		{"below minimum", "steps=29 hooks=30 allows=15 denies=15 errors=15 files=15 batches=2 repeats=0", func(in *agyCheckObserveInput) {
			lines := strings.SplitAfter(string(in.Transcript), "\n")
			in.Transcript = []byte(strings.Join(append(lines[:28], lines[29:]...), ""))
		}},
		{"unknown reply", "unparsed transcript", func(in *agyCheckObserveInput) { in.Replies[0] = "UNTRUSTED_TRANSCRIPT_MARKER" }},
		{"non alternating", "steps=30 hooks=30 allows=15 denies=15 errors=15 files=15 batches=2 repeats=28", func(in *agyCheckObserveInput) {
			in.Replies = append(strings.Fields(strings.Repeat("allow ", 15)), strings.Fields(strings.Repeat("deny ", 15))...)
		}},
		{"duplicate file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[1] = in.CreatedFiles[0] }},
		{"unprefixed file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[0] = "UNTRUSTED_TRANSCRIPT_MARKER" }},
		{"slash file", "unparsed transcript", func(in *agyCheckObserveInput) { in.CreatedFiles[0] = "created-/UNTRUSTED_TRANSCRIPT_MARKER" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := agyCheckObserveFixtures()
			in := inputs["reconciliation"]
			tc.change(&in)
			inputs["reconciliation"] = in
			got := agyCheckObserveRow(agyCheckExpected[26], in)
			if tc.want == "unparsed transcript" && got != (agyCheckObservation{ID: "reconciliation", Outcome: "unparsed transcript"}) {
				t.Fatalf("summary failure retained facts or echoed content: %+v", got)
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
			beforeR, before := agyCheckObserveEvaluate(t, agyCheckObserveFixtures())
			_ = agyCheckBuiltinInfo([]byte(tc.listing))
			afterR, after := agyCheckObserveEvaluate(t, agyCheckObserveFixtures())
			if agyCheckExitCode(beforeR) != 0 || agyCheckExitCode(afterR) != 0 || before != after || strings.Contains(after, "INFO builtin/") {
				t.Fatal("builtin information altered exit code or report")
			}
		})
	}
}
