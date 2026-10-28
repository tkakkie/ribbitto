package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzAggregator compares the independent whole-stream oracle with chunked
// writes. Cutting writes inside UTF-8 and JSON escapes must not change evidence.
// Approved stricter identity/field caps have explicit A2/B2 rows; missing-result
// diagnostics use sorted packages, package result before tests in both parsers.
func FuzzAggregator(f *testing.F) {
	seeds := []string{
		containerEvents("p", "TestOne", "pass"),
		containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"),
		containerJSON("p", "start", "run/TestParent", "pause/TestParent", "cont/TestParent", "run/TestParent/child", "skip/TestParent/child", "pass/TestParent", "pass"),
		containerJSON("p", "start") + `{"Time":"2026-10-28T00:00:00Z","Action":"output","Package":"p","Elapsed":0,"FailedBuild":"q","Output":"hello\n"}` + "\n" + containerJSON("p", "pass"),
		"", "null\n", "garbage\n", "garbage\n{", "{} \n", `{"Action":"pass","ACTION":"fail"}` + "\n",
	}
	for _, e := range measurementSkips() {
		seeds = append(seeds, measurementStream(e, e.message, true))
	}
	for _, seed := range seeds {
		f.Add([]byte(seed), uint16(len(seed)))
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk uint16) {
		if len(data) >= containerOutputLimit {
			return
		}
		a := newContainerAggregator(nil, measurementSkips())
		step := max(1, int(chunk))
		for start := 0; start < len(data); start += step {
			_, _ = a.stdoutWriter().Write(data[start:min(len(data), start+step)])
		}
		got := aggregatorFinish(a)
		want := oracleVerdict(0, nil, data, false, measurementSkips())
		wantFirst, _, _ := strings.Cut(want, "\n")
		if aggregatorFirst(got) == "verified" && wantFirst != "verified" {
			t.Fatalf("weaker than oracle: got %q want %q", got.verdict, want)
		}
		// Named, approved differential exception: pin both exact first lines,
		// in addition to the universal never-verified-where-oracle-refuses guard.
		if string(data) == "garbage\n{" {
			assertAggregatorE5CutException(t, got, want)
		}
		// N1 gives an already malformed line precedence over a later cut line;
		// the frozen oracle checks the final newline before reading any event.
		cut := len(data) == 0 || data[len(data)-1] != '\n'
		precedenceConflict := cut && a.record.stop != stopTruncated && a.record.capName == ""
		stricter := a.record.capName != "" || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
		if !stricter && precedenceConflict {
			if !strings.HasPrefix(got.verdict, "FAILED:") || wantFirst != "FAILED: empty or truncated test stream" || !a.record.stop.malformed() {
				t.Fatalf("precedence exception must be a malformed FAILED: %q stop=%s", got.verdict, a.record.stop)
			}
		}
		if !stricter && !precedenceConflict && got.verdict != want {
			t.Fatalf("differential: got %q want %q", got.verdict, want)
		}
		if !stricter && !precedenceConflict && a.record.complete {
			facts, confirmed := oracleCompleteFacts(data, measurementSkips())
			if confirmed != a.record.confirmedFailures || facts.events != a.record.events || facts.packages != a.record.packages || facts.leaves != a.record.leaves || strings.Join(facts.allowed, "\n") != strings.Join(a.record.allowed, "\n") || strings.Join(facts.skipped, "\n") != strings.Join(a.skipped, "\n") {
				t.Fatal("differential counts or skip names")
			}
		}
	})
}
