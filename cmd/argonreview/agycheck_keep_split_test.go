package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Independent synthetic collection outcomes exercise both terms even though
// production collection normally makes invalid accounting fail both rows.
func TestAgyCheckKeepSplit(t *testing.T) {
	for _, tc := range []struct {
		name                string
		accounting, batches bool
	}{
		{"both-ok", true, true}, {"accounting-only", false, true},
		{"batches-only", true, false}, {"both-differ", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckNewRunner(t)
			h.o.calls, h.o.keepTranscripts = 4, true
			o := agyCheckObservation{ID: "reconciliation", ParallelOutcome: "sequential",
				Summary: agyCheckSummary{ToolSteps: 4, HookInvocations: 4, Allows: 2, Denies: 2, Errors: 2, FilesCreated: 2}}
			if !tc.batches {
				o.ParallelOutcome = "parallel: 2 overlapping batches"
			}
			if !tc.accounting {
				if tc.batches {
					o.Outcome = "sequential"
				} else {
					o.Outcome = "synthetic invalid"
				}
			}
			a, b := agyCheckClassify(o, 4), agyCheckClassify(agyCheckParallelObservation(o), 4)
			if a.OK != tc.accounting || b.OK != tc.batches {
				t.Fatal("invalid test setup")
			}
			var results agyCheckRowResults
			observation := agyCheckObservation{ID: "reconciliation", Outcome: "collection failed"}
			row := agyCheckObserveFindRow(t, "reconciliation")
			agyCheckScriptFixture(t, h.o.agy, h.script)
			_, err := agyCheckRunAgy(context.Background(), h.o, h.d, h.m, h.r, h.script.Prompt, h.config,
				map[string][]byte{".agent/hooks.json": []byte("workspace")}, h.limit, h.ops,
				func(agyCheckRowDirs) error { observation = o; return nil },
				agyCheckRetainer(h.o, h.m.Isolation, h.d.files, row, &observation, &results))
			if err != nil || h.starts != 3 {
				t.Fatalf("fake run: %v starts=%d", err, h.starts)
			}
			want := []string{}
			if !tc.accounting || !tc.batches {
				want = []string{"reconciliation"}
			}
			if len(results.kept) != len(want) || len(want) == 1 && results.kept[0] != "reconciliation" {
				t.Fatalf("initial retention: %v want %v", results.kept, want)
			}
			// Force a provisional copy for the both-ok case, so final pruning is
			// independently checked for all four pairs as well as initial selection.
			if len(results.kept) == 0 {
				m, r, f := agyCheckTestRow(t)
				for _, name := range []string{"out.jsonl", "err.txt"} {
					agyCheckTestWrite(t, filepath.Join(r.Row, name), []byte(agyCheckKeepMarker), 0o600)
				}
				if _, err := agyCheckKeepRow(m.Isolation, r.Row, "reconciliation", f); err != nil {
					t.Fatal(err)
				}
				h.m.Isolation, h.d.files = m.Isolation, f
			}
			result := agyCheckFinishKeep(h.o, h.m.Isolation, h.d.files, []string{"reconciliation"}, agyCheckResult{Rows: []agyCheckResultRow{a, b}})
			var report bytes.Buffer
			if err := agyCheckRender(&report, result); err != nil {
				t.Fatal(err)
			}
			suffix := "kept transcripts: " + agyCheckKeptText(want) + "\n"
			if result.InvalidReason != "" || !reflect.DeepEqual(result.KeptRows, want) || !strings.HasSuffix(report.String(), suffix) {
				t.Fatalf("final retention: %+v report=%s", result, report.String())
			}
			entries, err := os.ReadDir(filepath.Join(h.m.Isolation, "kept"))
			if err != nil || len(entries) != len(want) || len(want) == 1 && entries[0].Name() != "reconciliation" {
				t.Fatalf("retained directories: %v %v", entries, err)
			}
		})
	}
}

// Real collection from the row fake checks the split's reachable combinations.
func TestAgyCheckKeepSplitCollected(t *testing.T) {
	for _, tc := range []struct {
		name, deviation string
		keep            bool
	}{{"both-ok", "real-hooks-sequential", false}, {"batches-only", "real-hooks", true}, {"both-differ", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckNewCommandHarness(t, "reconciliation", tc.deviation)
			h.x.runner.check = h.d
			o := agyCheckOptions{home: h.m.Home, agy: h.agy, model: agyCheckModel, calls: 4, keepTranscripts: true}
			var results agyCheckRowResults
			if err := agyCheckExecuteRow(o, h.x, h.m, h.tool, agyCheckObserveFindRow(t, "reconciliation"), &results); err != nil {
				t.Fatal(err)
			}
			result := agyCheckAssemble(h.m, h.x.runner.files, results, 4)
			result = agyCheckFinishKeep(o, h.m.Isolation, h.x.runner.files, results.kept, result)
			if result.InvalidReason != "" || len(result.KeptRows) != len(results.kept) || (len(result.KeptRows) == 1) != tc.keep {
				t.Fatalf("collected split retention: %+v", result)
			}
			if tc.keep {
				entries, err := os.ReadDir(filepath.Join(h.m.Isolation, "kept"))
				if err != nil || len(entries) != 1 || entries[0].Name() != "reconciliation" || result.KeptRows[0] != "reconciliation" {
					t.Fatalf("collected IDs/tree: %v %v %v", result.KeptRows, entries, err)
				}
			}
		})
	}
}
