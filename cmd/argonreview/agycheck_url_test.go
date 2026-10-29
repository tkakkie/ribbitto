package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAgyCheckURLRowEvidence(t *testing.T) {
	t.Parallel()
	handshake := []string{"start -", "request initialize", "response initialize", "request initialized", "request toolslist", "response toolslist", "request ping", "response ping"}
	for _, id := range []string{"read-url", "execute-url"} {
		row := agyCheckObserveFindRow(t, id)
		s, _ := agyCheckScenarioFor(id)
		cases := []struct {
			name, tool, claim, leaf, unreadable, want string
			stub                                      []string
		}{
			{name: "handshake", want: "not exercisable"},
			{name: "no-stub", stub: []string{}, want: "not exercisable"},
			{name: "start-only", stub: []string{"start -"}, want: "not exercisable"},
			{name: "view_file", tool: "view_file", want: "unexpected tool activity"},
			{name: "write_file", tool: "write_file", want: "unexpected tool activity"},
			{name: "manage_task", tool: "manage_task", want: "unexpected tool activity"},
			{name: "url-tool", tool: s.tool(), want: "unexpected tool activity"},
			{name: "unreadable-claims", unreadable: "state", want: "invalid call records"},
			{name: "unreadable-stub", unreadable: "stub", want: "invalid call records"},
			{name: "malformed-claim", claim: "malformed", want: "invalid call records"},
			{name: "gap", claim: "allow " + id, leaf: "000002", want: "invalid call records"},
			{name: "malformed-stub", stub: []string{"malformed"}, want: "invalid call records"},
			{name: "hook-claim", claim: "allow " + id, want: "unexpected hook claim"},
			{name: "response-alone", stub: []string{"start -", "response initialize"}, want: "invalid stub evidence"},
			{name: "pending-handshake", stub: []string{"start -", "request ping"}, want: "invalid stub evidence"},
			{name: "unknown-tag", stub: []string{"start -", "request unknown", "response unknown"}, want: "invalid stub evidence"},
		}
		for _, tag := range []string{"verify", "danger", "call", "list", "read"} {
			for _, kind := range []string{"request", "response", "pair"} {
				stub := append(append([]string(nil), handshake...), kind+" "+tag)
				if kind == "pair" {
					stub = append(append([]string(nil), handshake...), "request "+tag, "response "+tag)
				}
				cases = append(cases, struct {
					name, tool, claim, leaf, unreadable, want string
					stub                                      []string
				}{name: kind + "-" + tag, stub: stub, want: "invalid stub evidence"})
			}
		}
		for _, tc := range cases {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				m, r, f := agyCheckTestRow(t)
				transcript := []byte(agyCheckTestInit + agyCheckTestResult)
				out := filepath.Join(r.Row, "out.jsonl")
				writeStub := func(lines []string) {
					t.Helper()
					calls := filepath.Join(r.Stub, "calls")
					if err := os.RemoveAll(calls); err != nil {
						t.Fatal(err)
					}
					for i, line := range lines {
						agyCheckTestWrite(t, filepath.Join(calls, fmt.Sprintf("%06d", i+1)), []byte(line+"\n"), 0o600)
					}
				}
				observe := func() agyCheckObservation {
					t.Helper()
					e, diagnostic, err := agyCheckCollect(m.Isolation, r, s, f)
					if err != nil {
						t.Fatal(err)
					}
					if diagnostic != "" {
						return agyCheckObservation{ID: id, Outcome: diagnostic}
					}
					return agyCheckEvidenceObservation(row, s, e)
				}
				agyCheckTestWrite(t, out, transcript, 0o600)
				writeStub(handshake)
				baseline := observe()
				if baseline.Outcome != "not exercisable" || !agyCheckClassify(baseline, 30).OK {
					t.Fatalf("URL baseline differs: %+v", baseline)
				}
				if tc.tool != "" {
					step := agyCheckTestInit + agyCheckTestStep(0, tc.tool, "ERROR") + agyCheckTestResult
					agyCheckTestWrite(t, out, []byte(step), 0o600)
				}
				if tc.claim != "" {
					leaf := tc.leaf
					if leaf == "" {
						leaf = "000001"
					}
					agyCheckTestWrite(t, filepath.Join(r.State, "calls", leaf), []byte(tc.claim+"\n"), 0o600)
				}
				if tc.stub != nil {
					writeStub(tc.stub)
				}
				if tc.unreadable != "" {
					stat := f.lstat
					f.lstat = func(path string) (os.FileInfo, error) {
						if path == filepath.Join(r.Row, tc.unreadable) {
							return nil, os.ErrPermission
						}
						return stat(path)
					}
				}
				got := observe()
				if got.Outcome != tc.want || agyCheckClassify(got, 30).OK != (tc.want == "not exercisable") {
					t.Fatalf("URL evidence guard lost: %s: observation=%+v want=%q", tc.name, got, tc.want)
				}
			})
		}
	}
}

func TestAgyCheckNonURLCallRecords(t *testing.T) {
	t.Parallel()
	row, s, fixture := agyCheckEvidenceFixture(t, "task-off")
	m, r, f := agyCheckTestRow(t)
	agyCheckTestWrite(t, filepath.Join(r.Row, "out.jsonl"), fixture.Input.Transcript, 0o600)
	claim := filepath.Join(r.State, "calls/000001")
	agyCheckTestWrite(t, claim, []byte("allow task-off\n"), 0o600)
	e, diagnostic, err := agyCheckCollect(m.Isolation, r, s, f)
	if err != nil || diagnostic != "" || !agyCheckClassify(agyCheckEvidenceObservation(row, s, e), 30).OK {
		t.Fatalf("non-URL baseline differs: diagnostic=%q err=%v", diagnostic, err)
	}
	agyCheckTestWrite(t, claim, []byte("malformed record\n"), 0o600)
	_, diagnostic, err = agyCheckCollect(m.Isolation, r, s, f)
	if err != nil || diagnostic != "invalid call records" {
		t.Fatalf("non-URL invalid records accepted: diagnostic=%q err=%v", diagnostic, err)
	}
}
