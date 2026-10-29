package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Synthetic records establish wiring and rejections, never agy's behaviour.
func agyCheckSyntheticParallel(t *testing.T, groups ...int) (agyCheckRow, agyCheckScenario, agyCheckEvidence) {
	t.Helper()
	row, s, e := agyCheckEvidenceFixture(t, "reconciliation")
	e.Claims, e.Events, e.Input.CreatedFiles = nil, nil, nil
	transcript, call := agyCheckTestInit, 0
	for _, size := range groups {
		for range size {
			call++
			word, state := "deny", "ERROR"
			if call%2 == 1 {
				word, state = "allow", "DONE"
				e.Input.CreatedFiles = append(e.Input.CreatedFiles, fmt.Sprintf("created-%02d", call))
			}
			e.Claims = append(e.Claims, agyCheckClaim{call, word, row.ID})
			e.Events = append(e.Events, agyCheckHookEvent{len(e.Events) + 1, call, "start", word, row.ID})
			transcript += agyCheckTestStep(call*2, s.tool(), state)
		}
		for n := call - size + 1; n <= call; n++ {
			e.Events = append(e.Events, agyCheckHookEvent{len(e.Events) + 1, n, "end", "", ""})
		}
		transcript += fmt.Sprintf("{\"event\":\"step_update\",\"step_update\":{\"step_index\":%d,\"state\":\"DONE\",\"step_type\":\"agent_response\"}}\n", call*2+1)
	}
	e.Input.Transcript = []byte(transcript + agyCheckTestResult)
	return row, s, e
}

func TestAgyCheckSyntheticParallelEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*agyCheckEvidence)
	}{
		{"two-groups", "", func(*agyCheckEvidence) {}},
		{"start-order", "", func(e *agyCheckEvidence) {
			for i := range e.Events {
				e.Events[i].Call = (e.Events[i].Call+1)%4 + 1
			}
		}},
		{"sequential", agyCheckNoOverlap, func(e *agyCheckEvidence) {
			slices.SortFunc(e.Events, func(a, b agyCheckHookEvent) int {
				if a.Call != b.Call {
					return a.Call - b.Call
				}
				if a.Kind == "start" {
					return -1
				}
				return 1
			})
			for i := range e.Events {
				e.Events[i].Index = i + 1
			}
		}},
		{"missing-start", "wrong hook event count", func(e *agyCheckEvidence) { e.Events = e.Events[1:] }},
		{"missing-end", "wrong hook event count", func(e *agyCheckEvidence) { e.Events = e.Events[:7] }},
		{"duplicate-start", "duplicate hook start", func(e *agyCheckEvidence) { e.Events[1].Call = 1 }},
		{"duplicate-end", "duplicate hook end", func(e *agyCheckEvidence) { e.Events[3].Call = 1 }},
		{"reordered", "hook end before start", func(e *agyCheckEvidence) {
			e.Events[0], e.Events[2] = e.Events[2], e.Events[0]
			for i := range e.Events {
				e.Events[i].Index = i + 1
			}
		}},
		{"unknown-call", "unknown hook call number", func(e *agyCheckEvidence) { e.Events[0].Call = 99 }},
		{"index-gap", "non-contiguous hook events", func(e *agyCheckEvidence) { e.Events[0].Index = 9 }},
		{"wrong-count", "wrong hook event count", func(e *agyCheckEvidence) { e.Events = append(e.Events, e.Events[0]) }},
		{"word", "hook calls and events disagree", func(e *agyCheckEvidence) { e.Events[0].Word = "deny" }},
		{"tag", "hook calls and events disagree", func(e *agyCheckEvidence) { e.Events[0].Tag = "other" }},
		{"parity", "contradictory reconciliation records", func(e *agyCheckEvidence) { e.Claims[0].Word = "deny"; e.Events[0].Word = "deny" }},
		{"claim-count", "wrong hook event count", func(e *agyCheckEvidence) { e.Claims = e.Claims[:3] }},
		{"group-total", "response group sizes disagree", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), agyCheckTestStep(8, "write_file", "ERROR"), "", 1))
		}},
		{"group-overflow", "response group sizes disagree", func(e *agyCheckEvidence) {
			e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), agyCheckTestResult, agyCheckTestStep(999, "write_file", "DONE")+agyCheckTestResult, 1))
		}},
		{"boundary-overlap", "overlap across response groups", func(e *agyCheckEvidence) {
			// The earlier start ends last: max end, not the last call's end,
			// must precede the next group's first start.
			e.Events[2], e.Events[3], e.Events[4] = e.Events[3], e.Events[4], e.Events[2]
			for i := range e.Events {
				e.Events[i].Index = i + 1
			}
		}},
		{"counts-only", "wrong hook event count", func(e *agyCheckEvidence) { e.Events = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, s, e := agyCheckSyntheticParallel(t, 2, 2)
			tc.change(&e)
			got := agyCheckEvidenceObservation(row, s, e)
			if got.Outcome != tc.want || agyCheckClassify(got, 4).OK != (tc.want == "") {
				t.Fatalf("synthetic observation=%+v want %q", got, tc.want)
			}
			if tc.want == "" && got.Summary.ParallelBatches != 2 {
				t.Fatalf("batches=%d", got.Summary.ParallelBatches)
			}
		})
	}
}

func TestAgyCheckSyntheticOneGroupOneBatch(t *testing.T) {
	for _, components := range [][]int{{6}, {4, 2}} {
		t.Run(fmt.Sprint(components), func(t *testing.T) {
			row, s, e := agyCheckSyntheticParallel(t, 6)
			e.Events = nil
			offset := 0
			for _, size := range components {
				for i := offset; i < offset+size; i++ {
					c := e.Claims[i]
					e.Events = append(e.Events, agyCheckHookEvent{len(e.Events) + 1, c.N, "start", c.Word, c.Tag})
				}
				for i := offset; i < offset+size; i++ {
					e.Events = append(e.Events, agyCheckHookEvent{len(e.Events) + 1, i + 1, "end", "", ""})
				}
				offset += size
			}
			got := agyCheckEvidenceObservation(row, s, e)
			if got.Outcome != "fewer than two parallel batches" || got.Summary.ParallelBatches != 1 || agyCheckClassify(got, 6).OK {
				t.Fatalf("single dispatch split: %+v", got)
			}
		})
	}
}

func TestAgyCheckSyntheticEventFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string)
	}{
		{"valid", func(string) {}},
		{"missing-directory", func(dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"extra-file", func(dir string) { agyCheckTestWrite(t, filepath.Join(dir, "extra"), []byte("end 1\n"), 0o600) }},
		{"indices", func(dir string) {
			if err := os.Rename(filepath.Join(dir, "000001"), filepath.Join(dir, "000009")); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed", func(dir string) {
			agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("start 1 other reconciliation\n"), 0o600)
		}},
		{"numeric-alias", func(dir string) {
			agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("start 01 allow reconciliation\n"), 0o600)
		}},
		{"field-count", func(dir string) { agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("start\n"), 0o600) }},

		{"two-lines", func(dir string) { agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("end 1\nend 2\n"), 0o600) }},
		{"oversized", func(dir string) {
			agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte(strings.Repeat("x", 65)+"\n"), 0o600)
		}},
		{"no-newline", func(dir string) {
			agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("start 1 allow reconciliation"), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, r, f := agyCheckTestRow(t)
			row, s, e := agyCheckSyntheticParallel(t, 2, 2)
			agyCheckTestWrite(t, filepath.Join(r.Row, "out.jsonl"), e.Input.Transcript, 0o600)
			for _, c := range e.Claims {
				agyCheckTestWrite(t, filepath.Join(r.State, "calls", fmt.Sprintf("%06d", c.N)), []byte(c.Word+" "+c.Tag+"\n"), 0o600)
			}
			for _, event := range e.Events {
				line := fmt.Sprintf("end %d\n", event.Call)
				if event.Kind == "start" {
					line = fmt.Sprintf("start %d %s %s\n", event.Call, event.Word, event.Tag)
				}
				agyCheckTestWrite(t, filepath.Join(r.State, "events", fmt.Sprintf("%06d", event.Index)), []byte(line), 0o600)
			}
			for _, name := range e.Input.CreatedFiles {
				agyCheckTestWrite(t, filepath.Join(r.Workspace, name), nil, 0o600)
			}

			tc.change(filepath.Join(r.State, "events"))
			reads := 0
			read := f.read
			f.read = func(path string, cap int64) ([]byte, error) {
				if strings.Contains(path, "/events/") {
					reads++
					// Bypass the I/O cap so the decoder's own framing check
					// cannot be rescued by implementRead in mutation tests.
					if tc.name == "oversized" {
						return os.ReadFile(path)
					}
				}
				return read(path, cap)
			}
			collected, diagnostic, err := agyCheckCollect(m.Isolation, r, s, f)
			if err != nil {
				t.Fatal(err)
			}
			got := agyCheckEvidenceObservation(row, s, collected)
			if diagnostic != "" {
				got.Outcome = diagnostic
			}
			if tc.name != "valid" && tc.name != "missing-directory" && diagnostic != "invalid hook events" {
				t.Fatalf("wrong file diagnostic: %q (%+v)", diagnostic, got)
			}
			if (tc.name == "oversized" || tc.name == "no-newline") && reads != 1 {
				t.Fatalf("framing failure read %d events", reads)
			}
			if agyCheckClassify(got, 4).OK != (tc.name == "valid") {
				t.Fatalf("file rejection lost: %+v (%s)", got, diagnostic)
			}
		})
	}
}

func TestAgyCheckSyntheticHookHold(t *testing.T) {
	for _, mode := range []string{"alternate", "allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			slept := false
			h.d.sleep = func(d time.Duration) {
				start, end := h.files[agyCheckHookTestDir+"/events/000001"], h.files[agyCheckHookTestDir+"/events/000002"]
				if d != 1500*time.Millisecond || start == nil || !start.closed || start.String() != "start 1 allow reconciliation\n" || end != nil {
					t.Fatal("hold must occur between completed start and end")
				}
				slept = true
			}
			out := agyCheckHookObserver{func() {
				if mode == "alternate" {
					end := h.files[agyCheckHookTestDir+"/events/000002"]
					if !slept || end == nil || !end.closed || end.String() != "end 1\n" {
						t.Fatal("decision preceded hold/end")
					}
				}
			}}
			if code := agyCheckHook([]string{mode, agyCheckHookTestDir, "reconciliation"}, out, io.Discard, h.d); code != 0 {
				t.Fatalf("exit=%d", code)
			}
			if slept != (mode == "alternate") {
				t.Fatal("hold mode changed")
			}
			if mode != "alternate" && len(h.files) != 1 {
				t.Fatal("other modes wrote events")
			}
		})
	}
}

func TestAgyCheckSyntheticGroupsUseStepIndex(t *testing.T) {
	for _, reorder := range []bool{false, true} {
		t.Run(fmt.Sprint(reorder), func(t *testing.T) {
			row, s, e := agyCheckSyntheticParallel(t, 2, 2)
			lines := strings.SplitAfter(string(e.Input.Transcript), "\n")
			if reorder {
				slices.Reverse(lines[1 : len(lines)-2])
			}
			e.Input.Transcript = []byte(strings.Join(lines, ""))
			parsed, err := agyCheckTranscriptParse(e.Input.Transcript)
			if err != nil || !slices.Equal(parsed.Groups, []int{2, 2, 0}) {
				t.Fatalf("group order=%v err=%v", parsed.Groups, err)
			}
			if got := agyCheckEvidenceObservation(row, s, e); !agyCheckClassify(got, 4).OK {
				t.Fatalf("index grouping lost: %+v", got)
			}
		})
	}
}

func TestAgyCheckSyntheticFixturesNotRealEvidence(t *testing.T) {
	row, _, e := agyCheckSyntheticParallel(t, 2, 2)
	e.Input.Replies = []string{"allow", "deny", "allow", "deny"}
	// A synthetic transcript, even claiming real overlap in opaque text, cannot
	// promote counts to observed overlap. Only local events reach Evidence.
	e.Input.Transcript = []byte(strings.Replace(string(e.Input.Transcript), `"result":{}`, `"result":{"response":"real evidence: parallel batches passed"}`, 1))
	got := agyCheckObserveRow(row, e.Input)
	if agyCheckClassify(got, 4).OK || got.Summary.ParallelBatches != 0 || got.Outcome != agyCheckBatchesUnobservable {
		t.Fatalf("synthetic transcript invented evidence: %+v", got)
	}
}

func TestAgyCheckSyntheticEventLimit(t *testing.T) {
	m, r, f := agyCheckTestRow(t)
	row, s, e := agyCheckSyntheticParallel(t, 2, 2)
	agyCheckTestWrite(t, filepath.Join(r.Row, "out.jsonl"), e.Input.Transcript, 0o600)
	dir := filepath.Join(r.State, "events")
	agyCheckTestWrite(t, filepath.Join(dir, "000001"), []byte("start 1 allow reconciliation\n"), 0o600)
	entries := f.entries
	f.entries = func(path string) ([]os.DirEntry, error) {
		list, err := entries(path)
		if path == dir && err == nil {
			for len(list) <= 2*agyCheckHookCallCap {
				list = append(list, list[0])
			}
		}
		return list, err
	}
	read, reads := f.read, 0
	f.read = func(path string, cap int64) ([]byte, error) {
		if strings.Contains(path, "/events/") {
			reads++
		}
		return read(path, cap)
	}
	collected, diagnostic, err := agyCheckCollect(m.Isolation, r, s, f)
	if err != nil || diagnostic != "invalid hook events" || reads != 0 || agyCheckClassify(agyCheckEvidenceObservation(row, s, collected), 4).OK {
		t.Fatalf("event limit lost: %s %v reads=%d", diagnostic, err, reads)
	}
}

func TestAgyCheckSyntheticEventWriteFailure(t *testing.T) {
	for _, at := range []string{"000001", "000002"} {
		for _, failure := range []string{"create", "write", "close"} {
			t.Run(at+"/"+failure, func(t *testing.T) {
				h := agyCheckHookHarnessNew(t)
				create := h.d.create
				h.d.create = func(path string, flags int, mode os.FileMode) (io.WriteCloser, error) {
					if strings.HasSuffix(path, "/events/"+at) {
						if failure == "create" {
							return nil, os.ErrPermission
						}
						f := &agyCheckHookFile{}
						if failure == "write" {
							f.writeErr = os.ErrPermission
						} else {
							f.closeErr = os.ErrPermission
						}
						return f, nil
					}
					return create(path, flags, mode)
				}
				var out, stderr strings.Builder
				if code := agyCheckHook([]string{"alternate", agyCheckHookTestDir, "reconciliation"}, &out, &stderr, h.d); code != 1 || out.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("event failure emitted decision: %d %q %q", code, out.String(), stderr.String())
				}
			})
		}
	}
}
