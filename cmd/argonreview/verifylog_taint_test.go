package main

import (
	"strings"
	"testing"
)

func TestVerifyLogAsServed(t *testing.T) {
	for _, sequence := range []struct{ name, text string }{
		{"OSC BEL", "\x1b]hidden\a"}, {"OSC ST", "\x1b]hidden\x1b\\"},
		{"OSC unterminated", "\x1b]hidden"},
		{"DCS terminated", "\x1bPhidden\x1b\\"}, {"DCS unterminated", "\x1bPhidden"},
		{"PM terminated", "\x1b^hidden\x1b\\"}, {"PM unterminated", "\x1b^hidden"},
		{"APC terminated", "\x1b_hidden\x1b\\"}, {"APC unterminated", "\x1b_hidden"},
		{"trailing ESC", "\x1b"}, {"dangling CSI", "\x1b["},
		{"C1 CSI", "\u009b"}, {"C1 OSC terminated", "\u009dhidden\u009c"},
		{"C1 OSC unterminated", "\u009dhidden"},
	} {
		for _, boundary := range []string{"line", "head-tail", "package-compiler"} {
			t.Run(sequence.name+"/"+boundary, func(t *testing.T) {
				const token = "g\x1b[mhp_placeholder"
				parts := []string{sequence.text, token}
				var visible strings.Builder
				for _, part := range parts {
					for line := range strings.SplitSeq(part, "\n") {
						visible.WriteString(verifyLogVisible(line))
						visible.WriteByte('\n')
					}
				}
				if !strings.Contains(visible.String(), "ghp_placeholder") {
					t.Fatal("oracle: next served line does not expose the placeholder")
				}
				r := verifyLogFixture("")
				if boundary == "package-compiler" {
					r.packageOutput.head, r.compiler.head = []byte(parts[0]), []byte(parts[1])
					if newVerifyLogCapture("FAILED", r).packageDetections == 0 {
						t.Fatal("package/compiler served line escaped the scan")
					}
					return
				}
				for _, output := range []struct {
					name string
					unit *retainedOutput
				}{{"entry", &r.entries[0].output}, {"stderr", &r.stderr}, {"stdout", &r.stdout}, {"package", &r.packageOutput}, {"compiler", &r.compiler}} {
					t.Run(output.name, func(t *testing.T) {
						if boundary == "line" {
							output.unit.head = []byte(strings.Join(parts, "\n"))
						} else {
							output.unit.head, output.unit.tail = []byte(parts[0]), []byte(parts[1])
						}
						if newVerifyLogCapture("FAILED", r).taint[output.unit] == 0 {
							t.Fatal("next served line escaped the scan")
						}
					})
				}
			})
		}
	}
}

func TestVerifyLogSegmentPasses(t *testing.T) {
	for _, boundary := range []string{"head-tail", "package-compiler"} {
		t.Run(boundary, func(t *testing.T) {
			r := verifyLogFixture("")
			// Neither segment contains a shape. Per-line sanitising followed by
			// a conservative join detects it; direct concatenation loses the h.
			if boundary == "head-tail" {
				r.entries[0].output.head, r.entries[0].output.tail = []byte("g\x1b["), []byte("hp_placeholder")
				if newVerifyLogCapture("FAILED", r).taint[&r.entries[0].output] == 0 {
					t.Fatal("head/tail split shape not detected")
				}
			} else {
				r.packageOutput.tail, r.compiler.head = []byte("g\x1b["), []byte("hp_placeholder")
				if newVerifyLogCapture("FAILED", r).packageDetections == 0 {
					t.Fatal("package/compiler split shape not detected")
				}
			}
		})
	}
	for _, row := range []struct {
		name, head, tail string
		want             int
	}{
		{"later segment", "safe", "ghp_placeholder", 1},
		{"independent joined pass", "\x1b]", "g\x1b\n[mhp_placeholder", 1},
		{"maximum not sum", "ghp_placeholder", "ghp_placeholder", 1},
		{"combined maximum", "ghp_placeholder", "AKIAplaceholder", 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			o := retainedOutput{head: []byte(row.head), tail: []byte(row.tail)}
			if got := verifyLogScanOutput(&o); got != row.want {
				t.Fatalf("segment maximum = %d, want %d", got, row.want)
			}
		})
	}
}

func TestVerifyLogNameAsServed(t *testing.T) {
	r := verifyLogFixture("")
	r.entries[0].name = "\x1b\ng\x1b[mhp_placeholder"
	r.entries[0].state = "pass" // Not indexed: the pre-membership scan must catch it.
	if !strings.Contains(summaryText(r.entries[0].name), "ghp_placeholder") {
		t.Fatal("oracle: summary name does not expose the placeholder")
	}
	if newVerifyLogCapture("FAILED", r).nameDetections == 0 {
		t.Fatal("as-served name escaped the scan")
	}
}
