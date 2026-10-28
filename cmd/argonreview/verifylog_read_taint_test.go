package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestVerifyLogReadTerminalBoundaries(t *testing.T) {
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
		for _, boundary := range []string{"entry", "stderr", "head-tail", "package-compiler", "output"} {
			t.Run(sequence.name+"/"+boundary, func(t *testing.T) {
				const disguised = "g\x1b[mhp_placeholder"
				r := verifyLogFixture("")
				stream, name := "test", "#1"
				o := &r.entries[0].output
				switch boundary {
				case "stderr":
					stream, name, o = "stderr", "", &r.stderr
				case "output":
					r.command = true
					stream, name, o = "output", "", &r.stdout
				case "package-compiler":
					r.entries[0].kind, r.entries[0].name = "package", ""
					stream, o = "package", &r.packageOutput
				}
				switch boundary {
				case "head-tail":
					// The retained gap ends the first served line without a newline.
					o.head, o.tail = []byte("before"+sequence.text), []byte(disguised)
					o.total = len(o.head) + len(o.tail) + len("omitted")
				case "package-compiler":
					o.head, r.compiler.head = []byte("before"+sequence.text), []byte(disguised)
					o.total, r.compiler.total = len(o.head), len(r.compiler.head)
				default:
					o.head = []byte("before" + sequence.text + "\n" + disguised + "\nafter")
					o.total = len(o.head)
				}
				verifyLogAssertTaintedReads(t, r, stream, name, false)
			})
		}
	}
}

func TestVerifyLogReadSplitTerminalBoundaries(t *testing.T) {
	for _, boundary := range []string{"head-tail", "package-compiler"} {
		t.Run(boundary, func(t *testing.T) {
			r := verifyLogFixture("")
			stream := "test"
			// Independent segment scans cannot see this shape. A direct join
			// consumes h as a CSI terminator; served lines reset before h.
			if boundary == "head-tail" {
				r.entries[0].output = retainedOutput{head: []byte("g\x1b["), tail: []byte("hp_placeholder"), total: len("g\x1b[hp_placeholder")}
			} else {
				stream = "package"
				r.entries[0].kind, r.entries[0].name = "package", ""
				r.packageOutput = retainedOutput{head: []byte("g\x1b["), total: len("g\x1b[")}
				r.compiler = retainedOutput{head: []byte("hp_placeholder"), total: len("hp_placeholder")}
			}
			verifyLogAssertTaintedReads(t, r, stream, "#1", true)
		})
	}
}

func verifyLogAssertTaintedReads(t *testing.T, record *containerRecord, stream, name string, split bool) {
	t.Helper()
	c := newVerifyLogCapture("FAILED", record)
	lines, _, class := c.unit(verifyLogArguments{stream: stream, name: name})
	if class != verifyLogOK {
		t.Fatalf("fixture unit: %s", class)
	}
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, strings.TrimPrefix(verifyLogLine(line), "| "))
	}
	separator := "\n"
	if split {
		separator = ""
	}
	if !strings.Contains(strings.Join(rendered, separator), "ghp_placeholder") {
		t.Fatal("sanity: rendered data lines would not show the placeholder")
	}
	// Cover every data/notice start, the first out-of-range start and a far
	// out-of-range start. Fresh stores keep refusal budgets out of the oracle.
	starts := []int{1234567}
	for start := 1; start <= len(lines)+1; start++ {
		starts = append(starts, start)
	}
	for _, api := range []string{"read", "readRaw"} {
		for _, start := range starts {
			t.Run(fmt.Sprintf("%s/start=%d", api, start), func(t *testing.T) {
				s := &verifyLogStore{}
				capture := s.capture("FAILED", record)
				if capture == nil {
					t.Fatal("capture refused")
				}
				var reply verifyLogReply
				var audit string
				if api == "readRaw" {
					reply, audit = s.readRaw(verifyLogArgsJSON(capture.id, stream, name, strconv.Itoa(start)), json.RawMessage(`1`))
				} else {
					reply, audit = s.read(verifyLogArguments{run: capture.id, stream: stream, name: name, start: start}, json.RawMessage(`1`), verifyLogOK)
				}
				verifyLogAssertWire(t, reply)
				if reply.class != verifyLogTainted || reply.detections == 0 || verifyLogReplyText(reply) != verifyLogRefusals()[verifyLogTainted] {
					t.Fatalf("reader returned %s, want whole-unit tainted refusal", reply.class)
				}
				for _, text := range []string{string(reply.line), audit} {
					if strings.Contains(text, "ghp_placeholder") || strings.Contains(text, "ghp_") {
						t.Fatal("placeholder leaked through the reader")
					}
				}
			})
		}
	}
}
