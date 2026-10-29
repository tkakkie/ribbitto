package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestAgyCheckStubPairing(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"mcp-on", "mcp-off", "list-resources", "read-resource"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			cases := map[string]func([]agyCheckStubRecord) []agyCheckStubRecord{
				"empty":         func([]agyCheckStubRecord) []agyCheckStubRecord { return nil },
				"missing-start": func(r []agyCheckStubRecord) []agyCheckStubRecord { return r[1:] },
				"second-start": func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "start", Tag: "-"})
				},
				"number-gap": func(r []agyCheckStubRecord) []agyCheckStubRecord { return r },
				"initialized-pending": func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "request", Tag: "initialized"}, agyCheckStubRecord{Kind: "response", Tag: "initialize"})
				},
				"duplicate-initialized-pending": func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "request", Tag: "initialized"}, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "request", Tag: "initialized"}, agyCheckStubRecord{Kind: "response", Tag: "initialize"})
				},
				"initialized-response": func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "response", Tag: "initialized"})
				},
			}
			if id == "mcp-on" || id == "mcp-off" {
				for _, tag := range []string{"verify", "danger", "call"} {
					cases["request-only-"+tag] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
						return append(r, agyCheckStubRecord{Kind: "request", Tag: tag})
					}
					cases["request-response-"+tag] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
						return append(r, agyCheckStubRecord{Kind: "request", Tag: tag}, agyCheckStubRecord{Kind: "response", Tag: tag})
					}
					cases["response-only-"+tag] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
						return append(r, agyCheckStubRecord{Kind: "response", Tag: tag})
					}
				}
			} else {
				cases["unpaired-target"] = func(r []agyCheckStubRecord) []agyCheckStubRecord { return r[:len(r)-1] }
				cases["unpaired-request"] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "request", Tag: "initialize"})
				}
				cases["response-without-request"] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "response", Tag: "initialize"})
				}
				cases["wrong-tag"] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "response", Tag: "other"})
				}
				cases["intervening-request"] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "response", Tag: "initialize"})
				}
				cases["out-of-order"] = func(r []agyCheckStubRecord) []agyCheckStubRecord {
					return append(r, agyCheckStubRecord{Kind: "response", Tag: "initialize"}, agyCheckStubRecord{Kind: "request", Tag: "initialize"}, agyCheckStubRecord{Kind: "response", Tag: "initialize"})
				}
				cases["missing-target"] = func(r []agyCheckStubRecord) []agyCheckStubRecord { return r[:3] }
				cases["repeated-target"] = func(r []agyCheckStubRecord) []agyCheckStubRecord { return append(r, r[3:]...) }
			}
			if id == "list-resources" || id == "read-resource" {
				t.Run("other-pairs", func(t *testing.T) {
					t.Parallel()
					row, s, e := agyCheckEvidenceFixture(t, id)
					for _, tag := range []string{"verify", "danger", "call"} {
						e.Stub = append(e.Stub, agyCheckStubRecord{len(e.Stub) + 1, "request", tag}, agyCheckStubRecord{len(e.Stub) + 2, "response", tag})
					}
					if !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4).OK {
						t.Fatal("allowed paired tokens refused")
					}
				})
			}
			for name, change := range cases {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					row, s, e := agyCheckEvidenceFixture(t, id)
					if id == "mcp-on" || id == "mcp-off" {
						e.Stub = append(e.Stub, agyCheckStubRecord{len(e.Stub) + 1, "request", "initialized"})
					}
					if !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4).OK {
						t.Fatal("stub baseline differs")
					}
					e.Stub = change(e.Stub)
					for i := range e.Stub {
						e.Stub[i].N = i + 1
					}
					if name == "number-gap" {
						e.Stub[1].N++
					}
					got := agyCheckEvidenceObservation(row, s, e)
					if got.Outcome != "invalid stub evidence" {
						t.Fatalf("stub pairing guard lost: %s: %+v", name, got)
					}
				})
			}
			for _, tc := range []struct {
				name      string
				positions []int
			}{
				{"handshake", []int{3}},
				{"before-first-pair", []int{1}},
				{"between-pairs", []int{3, 6}},
				{"after-pair", []int{5}},
				{"duplicate", []int{3, 3}},
				{"omitted", nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					row, s, e := agyCheckEvidenceFixture(t, id)
					// Give denied rows a second harmless pair too, so every row
					// exercises notifications before, between and after pairs.
					e.Stub = append(e.Stub, agyCheckStubRecord{Kind: "request", Tag: "toolslist"}, agyCheckStubRecord{Kind: "response", Tag: "toolslist"})
					for _, position := range tc.positions {
						e.Stub = slices.Insert(e.Stub, position, agyCheckStubRecord{Kind: "request", Tag: "initialized"})
					}
					for i := range e.Stub {
						e.Stub[i].N = i + 1
					}
					if got := agyCheckEvidenceObservation(row, s, e); !agyCheckClassify(got, 4).OK {
						t.Fatalf("standalone notification evidence refused: %s: %+v", tc.name, got)
					}
				})
			}
		})
	}
}

func TestAgyCheckDiscoveryCompletion(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"ERROR", "DONE"} {
		for _, ran := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ran-%t", state, ran), func(t *testing.T) {
				t.Parallel()
				row, s, e := agyCheckEvidenceFixture(t, "discovery-agent")
				if !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 4).OK {
					t.Fatal("discovery baseline differs")
				}
				e.Input.Transcript = []byte(strings.ReplaceAll(string(e.Input.Transcript), "DONE", state))
				e.Claims = nil
				if ran {
					e.Claims = []agyCheckClaim{{1, "allow", row.ID}}
				}
				got := agyCheckEvidenceObservation(row, s, e)
				want := ran
				if agyCheckClassify(got, 4).OK != want {
					t.Fatalf("discovery completion requirement lost: %+v", got)
				}
				if !want && got.Outcome != agyCheckLoadUnobservable {
					t.Fatalf("discovery failure diagnostic lost: %+v", got)
				}
			})
		}
	}
}

func TestAgyCheckRunObservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		run        agyCheckRunResult
	}{
		{"ordinary-nonzero", "baseline", agyCheckRunResult{Code: 1}},
		{"timeout", "run timeout", agyCheckRunResult{Code: 124}},
		{"straggler", "run left processes", agyCheckRunResult{Stragglers: []string{"process"}}},
		{"both", "run left processes", agyCheckRunResult{Code: 124, Stragglers: []string{"process"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row := agyCheckObserveFindRow(t, "hook-silent")
			got := agyCheckRunObservation(row, agyCheckObservation{ID: row.ID, Outcome: "baseline"}, tc.run)
			if got.ID != row.ID || got.Outcome != tc.want {
				t.Fatalf("run observation override lost: %+v want %s", got, tc.want)
			}
		})
	}
}

func TestAgyCheckCommandSignalInvalid(t *testing.T) {
	t.Parallel()
	h := agyCheckNewCommandHarness(t, agyCheckExpected[0].ID, "signal")
	code, out := h.run(t, 0)
	if code != 1 || !strings.Contains(out, "RESULT INVALID:") || strings.Contains(out, "Rows:") || strings.Contains(out, agyCheckSuccessLine) {
		t.Fatalf("signal became an observation: code=%d %s", code, out)
	}
	if h.starts != 3 {
		t.Fatalf("later row started after signal: starts=%d", h.starts)
	}
	h.restored(t)
}
