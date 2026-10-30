package main

import (
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"testing"
)

// Frozen from origin/main before buffer reuse. Only the reference function
// names differ; keep these passes independent of the optimized scanner.
func verifyLogScanReference(text string) int {
	// Keep the raw and joined passes for disguised or fragmented shapes. The
	// reader resets terminal parsing on every served line, so do that too.
	joined := strings.ReplaceAll(text, "\n", "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = verifyLogVisibleReference(line)
	}
	return max(verifyLogDetections(text), verifyLogDetections(joined),
		verifyLogDetections(verifyLogVisibleReference(joined)), verifyLogDetections(verifyLogVisibleReference(text)),
		verifyLogDetections(strings.Join(lines, "")), verifyLogDetections(strings.Join(lines, "\n")))
}

func verifyLogVisibleReference(s string) string {
	var b strings.Builder
	verifyLogText(s, func(r rune, _ int) {
		if !verifyLogInvisible(r) {
			b.WriteRune(r)
		}
	})
	return b.String()
}

func verifyLogScanSegmentsReference(segments ...string) int {
	n := 0
	for _, segment := range segments {
		n = max(n, verifyLogScanReference(segment))
	}
	// A gap is not reconstructed evidence. A newline preserves terminal resets
	// while the joined-line passes still over-detect shapes split across gaps.
	return max(n, verifyLogScanReference(strings.Join(segments, "\n")))
}

func verifyLogReferenceShapes(t testing.TB) []string {
	t.Helper()
	_, database, _ := strings.Cut(containerTestDatabaseURL, "=")
	u, err := url.Parse(database)
	if err != nil || u.User == nil {
		t.Fatal("cannot derive CI tripwire")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		t.Fatal("missing CI tripwire")
	}
	return []string{"ghp_", "github_pat_", "AKIA", "-----BEGIN ", password}
}

type verifyLogReferenceCase struct{ name, text string }

func verifyLogReferenceCases(t testing.TB) []verifyLogReferenceCase {
	t.Helper()
	rows := []verifyLogReferenceCase{
		{"empty", ""}, {"newline", "\n"}, {"two-newlines", "\n\n"},
		{"only-newlines", strings.Repeat("\n", verifyLogPageLines)},
		{"trailing-escape", "ordinary\x1b"},
		{"above-display-cap", strings.Repeat("x", summaryLineLimit+1)},
		{"long-single-line", strings.Repeat("x", verifyLogDataBytes*4)},
		{"many-short-lines", strings.Repeat("x\n", verifyLogDataBytes*4)},
		// These expose the per-line joined pass after whole-text terminal
		// parsing consumes a character or an unterminated control string.
		{"joined-visible-only", "g\x1b[\nh\u200bp_"},
		{"later-line-visible-only", "\x1b]\ngh\x1b[31mp_"},
		{"line-joined-only", "gh\np_"},
		{"visible-only", "gh\x1b[31mp_"},
	}
	separators := []struct{ name, text string }{
		{"LF", "\n"}, {"CR", "\r"}, {"CSI", "\x1b[31m"},
		{"OSC-BEL", "\x1b]hidden\a"}, {"OSC-ST", "\x1b]hidden\x1b\\"},
		{"OSC-C1-ST", "\x1b]hidden\u009c"},
		{"DCS-BEL", "\x1bPhidden\a"}, {"DCS-ST", "\x1bPhidden\x1b\\"},
		{"DCS-C1-ST", "\x1bPhidden\u009c"},
		{"PM", "\x1b^hidden\x1b\\"}, {"APC", "\x1b_hidden\a"},
		{"lone-ESC-before-LF", "\x1b\n"},
		{"zero-width", "\u200b"}, {"bidi", "\u202e"},
		{"control", "\x00\x01\t\x7f"}, {"C1", "\u0080\u0085\u009c\u009f"},
		{"C1-CSI", "\u009b31m"}, {"C1-OSC", "\u009dhidden\u009c"},
		{"invalid-UTF8", "\xff\xfe\xc0\xaf\xe2\x82"}, {"replacement", "\ufffd"},
		{"four-byte", "😀"}, {"combining", "e\u0301"},
	}
	shapes := verifyLogReferenceShapes(t)
	rows = append(rows, verifyLogReferenceCase{"all-shapes-one-per-line", strings.Join(shapes, "\n")})
	for i, shape := range shapes {
		name := fmt.Sprintf("shape-%d/", i)
		for _, row := range []verifyLogReferenceCase{
			{"whole", shape}, {"one-byte-per-line", strings.Join(strings.Split(shape, ""), "\n")},
			{"trailing-newline", shape + "\n"}, {"leading-newline", "\n" + shape},
			{"trailing-escape", shape + "\x1b"},
			{"after-lone-escape", "\x1b\n" + shape},
			{"buffer-start", shape + strings.Repeat("x", verifyLogDataBytes*4)},
			{"buffer-end", strings.Repeat("x", verifyLogDataBytes*4) + shape},
			{"above-display-cap", strings.Repeat("x", summaryLineLimit+1) + shape},
			{"many-short-lines", strings.Repeat("x\n", verifyLogDataBytes*4) + shape},
			{"unicode-neighbors", "😀e\u0301\ufffd\xff" + shape + "\xfe\ufffde\u0301😀"},
		} {
			rows = append(rows, verifyLogReferenceCase{name + row.name, row.text})
		}
		for split := 1; split < len(shape); split++ {
			for _, separator := range separators {
				rows = append(rows, verifyLogReferenceCase{
					fmt.Sprintf("%ssplit-%d/%s", name, split, separator.name),
					shape[:split] + separator.text + shape[split:],
				})
			}
		}
	}
	return rows
}

func assertVerifyLogScanReference(t testing.TB, text string) {
	t.Helper()
	if got, want := verifyLogScan(text), verifyLogScanReference(text); got != want {
		// Never print corpus bytes: one tripwire comes from the CI credential.
		t.Fatalf("scan differential: bytes=%d got=%d want=%d", len(text), got, want)
	}
}

func assertVerifyLogSegmentsReference(t testing.TB, segments []string) {
	t.Helper()
	if got, want := verifyLogScanSegments(segments...), verifyLogScanSegmentsReference(segments...); got != want {
		t.Fatalf("segment differential: segments=%d got=%d want=%d", len(segments), got, want)
	}
}

func TestVerifyLogScanMatchesReference(t *testing.T) {
	for _, row := range verifyLogReferenceCases(t) {
		t.Run(row.name, func(t *testing.T) {
			assertVerifyLogScanReference(t, row.text)
			// Include empty segments and cuts within UTF-8 and terminal escapes.
			for _, split := range []int{0, len(row.text) / 2, len(row.text)} {
				assertVerifyLogSegmentsReference(t, []string{"", row.text[:split], row.text[split:], ""})
			}
		})
	}
	assertVerifyLogSegmentsReference(t, nil)
}

func TestVerifyLogScanReferenceCorpus(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x1981, 0x1913))
	fragments := []string{
		"", "\n", "\n\n", "\r", "\r\n", "\x1b", "\x1b[", "\x1b[31m", "\x1b[m",
		"\x1b]", "\x1b]hidden\a", "\x1b]hidden\x1b\\", "\x1b]hidden\u009c",
		"\x1bP", "\x1bPhidden\a", "\x1bPhidden\x1b\\", "\x1bPhidden\u009c",
		"\x1b^hidden\x1b\\", "\x1b_hidden\a", "\u009b31m", "\u009dhidden\u009c",
		"\u200b", "\u202e", "\u2066", "\ufeff", "\u0080\u0085\u009c\u009f",
		"\x00\x01\t\x7f", "\xff", "\xfe\xc0\xaf", "\xe2\x82", "\ufffd", "😀", "e\u0301",
		"x", "ordinary", " ", strings.Repeat("x", summaryLineLimit+1),
	}
	for _, shape := range verifyLogReferenceShapes(t) {
		fragments = append(fragments, shape)
		for split := 1; split < len(shape); split++ {
			fragments = append(fragments, shape[:split], shape[split:])
		}
	}
	for i := range verifyLogCaptures * verifyLogDataBytes * 2 {
		t.Run(fmt.Sprintf("input-%d", i), func(t *testing.T) {
			var b strings.Builder
			for range 1 + rng.IntN(summaryLineLimit) {
				b.WriteString(fragments[rng.IntN(len(fragments))])
			}
			text := b.String()
			assertVerifyLogScanReference(t, text)
			var segments []string
			start := 0
			for range 1 + rng.IntN(verifyLogCaptures*2) {
				end := start + rng.IntN(len(text)-start+1)
				segments = append(segments, text[start:end])
				start = end
			}
			segments = append(segments, text[start:])
			assertVerifyLogSegmentsReference(t, segments)
		})
	}
}

// FuzzVerifyLogScanMatchesReference preserves every detection pass across
// arbitrary bytes, including segment cuts within UTF-8 and terminal sequences.
func FuzzVerifyLogScanMatchesReference(f *testing.F) {
	for _, row := range verifyLogReferenceCases(f) {
		f.Add([]byte(row.text), uint16(len(row.text)/2))
	}
	f.Fuzz(func(t *testing.T, data []byte, split uint16) {
		text := string(data)
		assertVerifyLogScanReference(t, text)
		cut := int(split) % (len(text) + 1)
		assertVerifyLogSegmentsReference(t, []string{text[:cut], text[cut:]})
	})
}
