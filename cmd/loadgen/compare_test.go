package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func comparisonInput(kind, body string) string {
	field := "streams"
	if kind == "expected" {
		field = "messages"
	}
	return fmt.Sprintf(`{"header":{"version":1,"kind":%q,"organization_slug":"test","channel_id":"one","initial_cursor":9,"final_watermark":20},%q:%s}`, kind, field, body)
}

func comparisonStream(index int, sequences string, resets int) string {
	return fmt.Sprintf(`{"index":%d,"sequences":%s,"reset":%d,"reconnects":{"established":0,"503":0,"refused":0,"other":0}}`, index, sequences, resets)
}

func runComparison(t *testing.T, receipts, expected string, extra ...string) (string, error) {
	t.Helper()
	paths := []string{t.TempDir() + "/receipts.json", t.TempDir() + "/expected.json"}
	for i, content := range []string{receipts, expected} {
		if err := os.WriteFile(paths[i], []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := run(append(extra, "-compare", paths[0], paths[1]), &out)
	return out.String(), err
}

func TestCompareCounts(t *testing.T) {
	const two = `[{"sequence":10,"marker":""},{"sequence":11,"marker":""}]`
	const marker = "loadgenABCDEFGHIJKLMNOPQRSTUVWXYZ0Z"
	for _, tt := range []struct {
		name, streams, messages string
		want                    comparison
	}{
		{"worked example", "[" + comparisonStream(0, `{"10":{"arrivals":1,"marker":""},"12":{"arrivals":3,"marker":""}}`, 0) + "]", two,
			comparison{Missing: 1, MissingByStream: []streamMissing{{0, 1}}, StreamsAffected: 1, Unexpected: 1, ReplayedDuplicates: 2, RepeatedPairs: 1}},
		{"empty stream", "[" + comparisonStream(0, `{}`, 2) + "]", two,
			comparison{Missing: 2, MissingByStream: []streamMissing{{0, 2}}, StreamsAffected: 1, Resets: 2}},
		{"multiple streams and posts", "[" + comparisonStream(0, `{"10":{"arrivals":3,"marker":""}}`, 1) + "," + comparisonStream(1, `{"10":{"arrivals":2,"marker":""},"12":{"arrivals":1,"marker":""}}`, 2) + "]",
			fmt.Sprintf(`[{"sequence":10,"marker":%q},{"sequence":11,"marker":%q},{"sequence":12,"marker":%q},{"sequence":13,"marker":"loadgenABCDEFGHIJKLMNOPQRSTUVWXYZ1Z"}]`, marker, marker, marker),
			comparison{Missing: 5, MissingByStream: []streamMissing{{0, 3}, {1, 2}}, StreamsAffected: 2, ReplayedDuplicates: 3, RepeatedPairs: 2, DuplicatePosts: 1, Resets: 3}},
		{"single committed post", "[" + comparisonStream(0, `{"10":{"arrivals":1,"marker":""}}`, 0) + "]", fmt.Sprintf(`[{"sequence":10,"marker":%q}]`, marker),
			comparison{MissingByStream: []streamMissing{{0, 0}}}},
		{"empty run", "[]", "[]", comparison{MissingByStream: []streamMissing{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, watermark := range []string{"20", "21"} {
				receipts := strings.Replace(comparisonInput("receipts", tt.streams), `"final_watermark":20`, `"final_watermark":`+watermark, 1)
				out, err := runComparison(t, receipts, comparisonInput("expected", tt.messages))
				want := tt.want
				want.ReceiptsWatermark, want.ExpectedWatermark = 20, 20
				if watermark == "21" {
					want.ReceiptsWatermark, want.WatermarkDiffers = 21, 1
				}
				var got comparison
				if err != nil || json.Unmarshal([]byte(out), &got) != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("comparison = %s, error %v; want %+v", out, err, want)
				}
			}
		})
	}
}

func TestCompareZeroCursorAndWatermark(t *testing.T) {
	zero := strings.NewReplacer(`"initial_cursor":9`, `"initial_cursor":0`, `"final_watermark":20`, `"final_watermark":0`)
	out, err := runComparison(t, zero.Replace(comparisonInput("receipts", "["+comparisonStream(0, `{}`, 0)+"]")), zero.Replace(comparisonInput("expected", "[]")))
	var got comparison
	want := comparison{MissingByStream: []streamMissing{{0, 0}}}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("zero fields refused: %s, %v", out, err)
	}
}

func TestCompareRefusals(t *testing.T) {
	receipts := comparisonInput("receipts", "["+comparisonStream(0, `{"10":{"arrivals":1,"marker":""}}`, 0)+"]")
	expected := comparisonInput("expected", `[{"sequence":10,"marker":""}]`)
	for _, tt := range []struct{ name, old, replacement string }{
		{"version mismatch", `"version":1`, `"version":2`},
		{"organization mismatch", `"organization_slug":"test"`, `"organization_slug":"other"`},
		{"channel mismatch", `"channel_id":"one"`, `"channel_id":"two"`},
		{"cursor mismatch", `"initial_cursor":9`, `"initial_cursor":8`},
		{"kind", `"kind":"receipts"`, `"kind":"expected"`},
		{"syntax", `"arrivals":1`, `"arrivals":`},
		{"wrong type", `"arrivals":1`, `"arrivals":"1"`},
		{"zero arrivals", `"arrivals":1`, `"arrivals":0`},
		{"zero sequence", `"10":`, `"0":`},
		{"sequence alias", `"10":`, `"010":`},
		{"marker grammar", `"marker":""`, `"marker":"invalid"`},
		{"duplicate stream", `"streams":[`, `"streams":[` + comparisonStream(0, `{}`, 0) + `,`},
		{"duplicate streams key", `"streams":[`, `"streams":[` + comparisonStream(0, `{"99":{"arrivals":0,"marker":"bogus"}}`, 0) + `],"streams":[`},
		{"duplicate sequences key", `"sequences":`, `"sequences":{"99":{"arrivals":0,"marker":"bogus"}},"sequences":`},
		{"duplicate header key", `"header":`, `"header":{},"header":`},
		{"duplicate header field", `"version":1`, `"version":2,"version":1`},
		{"duplicate stream field", `"index":0`, `"index":1,"index":0`},
		{"duplicate sequence entry", `"10":`, `"10":{"arrivals":0,"marker":"bogus"},"10":`},
		{"duplicate receipt field", `"arrivals":1`, `"arrivals":0,"arrivals":1`},
		{"duplicate reconnect field", `"established":0`, `"established":1,"established":0`},
		{"null record", `"arrivals":1,"marker":""`, `"arrivals":null,"marker":""`},
		{"unknown field", `"reset":0`, `"reset":0,"extra":0`},
		{"trailing object", `}]}`, `}]} {}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runComparison(t, strings.Replace(receipts, tt.old, tt.replacement, 1), expected)
			if err == nil || out != "" {
				t.Fatalf("did not refuse before counting: %s, %v", out, err)
			}
		})
	}
	for _, input := range []string{receipts, expected} {
		for _, field := range []string{`"version":1,`, `"kind":"receipts",`, `"kind":"expected",`, `"organization_slug":"test",`, `"channel_id":"one",`, `"initial_cursor":9,`, `,"final_watermark":20`, `"index":0,`, `"reset":0,`, `"established":0,`, `"arrivals":1,`, `"sequence":10,`, `,"marker":""`} {
			if !strings.Contains(input, field) {
				continue
			}
			a, b := receipts, expected
			if input == receipts {
				a = strings.Replace(input, field, "", 1)
			} else {
				b = strings.Replace(input, field, "", 1)
			}
			if out, err := runComparison(t, a, b); err == nil || out != "" {
				t.Fatalf("accepted missing %s: %s, %v", field, out, err)
			}
		}
	}
	for _, bad := range []string{`[{"sequence":9,"marker":""}]`, `[{"sequence":21,"marker":""}]`, `[{"sequence":10,"marker":"invalid"}]`, `[{"sequence":9,"sequence":10,"marker":""}]`, `[{"sequence":10,"marker":""},{"sequence":10,"marker":""}]`, `[{"sequence":11,"marker":""},{"sequence":10,"marker":""}]`, `null`, `[null]`} {
		if out, err := runComparison(t, receipts, comparisonInput("expected", bad)); err == nil || out != "" {
			t.Fatalf("accepted malformed expected set: %s, %v", out, err)
		}
	}
	if out, err := runComparison(t, strings.Replace(receipts, `"version":1`, `"version":2`, 1), strings.Replace(expected, `"version":1`, `"version":2`, 1)); err == nil || out != "" {
		t.Fatalf("accepted unsupported shared version: %s, %v", out, err)
	}
	if _, err := runComparison(t, receipts, expected, "-tokens=unused"); err == nil {
		t.Fatal("accepted live flags with comparison")
	}
	if err := run([]string{"-compare", "unused"}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted missing expected path")
	}
	if err := run([]string{"-compare=", "e"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires receipts and expected files") {
		t.Fatalf("accepted an empty -compare: %v", err)
	}
	if err := run([]string{"-compare", "r", "e", "extra"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires receipts and expected files") {
		t.Fatalf("accepted extra comparison argument: %v", err)
	}
	if err := run([]string{"-compare", t.TempDir() + "/absent", "unused"}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted absent file")
	}
}
