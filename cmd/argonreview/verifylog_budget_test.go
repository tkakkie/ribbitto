package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyLogStreamBudgets(t *testing.T) {
	for _, row := range []struct {
		stream, name, shared, sharedName string
		limit                            int
	}{
		{"index", "", "index", "", verifyLogIndexReads},
		{"test", "#1", "package", "#2", verifyLogEntryReads},
		{"package", "#2", "test", "#1", verifyLogEntryReads},
		{"stderr", "", "output", "", verifyLogAuxReads},
		{"output", "", "stderr", "", verifyLogAuxReads},
	} {
		t.Run(row.stream, func(t *testing.T) {
			s := &verifyLogStore{}
			r := verifyLogFixture("evidence")
			r.command = true
			r.entries = append(r.entries, &aggregatorEntry{kind: "package", pkg: "p", state: "fail"})
			c := s.capture("FAILED", r)
			for i := 0; i <= row.limit; i++ {
				reply := verifyLogRead(t, s, c, row.stream, row.name, "1")
				if (reply.class == verifyLogOK) != (i < row.limit) {
					t.Fatalf("read %d class %s", i+1, reply.class)
				}
			}
			shared := verifyLogRead(t, s, c, row.shared, row.sharedName, "1")
			if shared.class != verifyLogBudget || verifyLogReplyText(shared) != "spent budget" {
				t.Fatalf("%s did not share %s budget: %s", row.shared, row.stream, shared.class)
			}
			// Exhausting one group leaves both other groups independent.
			for _, other := range []struct{ stream, name string }{{"index", ""}, {"test", "#1"}, {"stderr", ""}} {
				if other.stream == row.stream || other.stream == row.shared {
					continue
				}
				if verifyLogRead(t, s, c, other.stream, other.name, "1").class != verifyLogOK {
					t.Fatalf("%s budget coupled to %s", other.stream, row.stream)
				}
			}
		})
	}
}

func TestVerifyLogServerReadBudget(t *testing.T) {
	s := &verifyLogStore{}
	a := verifyLogArguments{run: strings.Repeat("0", verifyLogIDBytes*2), stream: "index", start: 1}
	for i := 0; i <= verifyLogServerReads; i++ {
		r, _ := s.read(a, json.RawMessage(`1`), verifyLogOK)
		want := verifyLogNoRun
		if i == verifyLogServerReads {
			want = verifyLogBudget
		}
		if r.class != want || s.reads != i+1 {
			t.Fatalf("server read %d: %s", i+1, r.class)
		}
	}
}

func TestVerifyLogServerByteBudget(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		s := &verifyLogStore{}
		c := s.capture("FAILED", verifyLogFixture(strings.Repeat("captured", verifyLogPrefixBytes)))
		if s.served != 0 || s.reads != 0 || s.bytes == 0 {
			t.Fatal("captured bytes counted as served")
		}
		a := verifyLogArguments{run: c.id, stream: "test", name: "missing", start: 1}
		wire := verifyLogRefuse(verifyLogMaxID(), verifyLogNotListed)
		s.served = verifyLogServerBytes - len(wire.line) + delta
		before := s.served
		r, _ := s.read(a, verifyLogMaxID(), verifyLogOK)
		want := verifyLogNotListed
		if delta > 0 {
			want = verifyLogBudget
		}
		if r.class != want || s.served != before+len(r.line) || s.reads != 1 {
			t.Fatalf("serialized refusal boundary delta=%d: %s", delta, r.class)
		}
	}
	s := &verifyLogStore{}
	c := s.capture("FAILED", verifyLogFixture("<>& output"))
	r := verifyLogRead(t, s, c, "test", "#1", "1")
	if s.served != len(r.line) || s.served == c.size || s.served == len(verifyLogReplyText(r)) {
		t.Fatal("budget did not count serialized response bytes")
	}
	s.served = verifyLogServerBytes
	c.taint[&c.record.entries[0].output] = 1
	spent := verifyLogRead(t, s, c, "test", "#1", "1")
	if spent.class != verifyLogBudget || spent.detections != 0 {
		t.Fatal("already spent bytes admitted a page")
	}
}

func TestVerifyLogRefusalsSpendCaptureBudget(t *testing.T) {
	for _, row := range []struct {
		stream, name, start, shared, sharedName string
		limit                                   int
		class                                   verifyLogClass
	}{
		{"index", "", strings.Repeat("9", verifyLogStartDigits), "index", "", verifyLogIndexReads, verifyLogRange},
		{"test", "absent", "1", "package", "#2", verifyLogEntryReads, verifyLogNotListed},
		{"package", "absent", "1", "test", "#1", verifyLogEntryReads, verifyLogNotListed},
		{"stderr", "", "1", "output", "", verifyLogAuxReads, verifyLogStderrDenied},
		{"output", "", "1", "stderr", "", verifyLogAuxReads, verifyLogStreamDenied},
	} {
		t.Run(row.stream, func(t *testing.T) {
			s := &verifyLogStore{}
			r := verifyLogFixture("evidence")
			r.command = row.stream != "output"
			r.stderrNotServable = row.stream == "stderr"
			r.entries = append(r.entries, &aggregatorEntry{kind: "package", pkg: "p", state: "fail"})
			c := s.capture("FAILED", r)
			for range row.limit {
				if reply := verifyLogRead(t, s, c, row.stream, row.name, row.start); reply.class != row.class {
					t.Fatalf("unexpected refusal: %s", reply.class)
				}
			}
			if verifyLogRead(t, s, c, row.shared, row.sharedName, "1").class != verifyLogBudget {
				t.Fatal("refusals did not consume shared per-capture budget")
			}
		})
	}
}

func TestVerifyLogAudit(t *testing.T) {
	s := &verifyLogStore{}
	c := s.capture("FAILED", verifyLogFixture("ghp_placeholder"))
	a := verifyLogArguments{run: c.id, stream: "test", name: "#1", start: 1}
	r, line := s.read(a, json.RawMessage(`1`), verifyLogOK)
	if !strings.Contains(line, c.id[:verifyLogAuditIDDigits]) || strings.Contains(line, c.id) || strings.Contains(line, "ghp_") || strings.Contains(line, "TestBad") || r.detections == 0 {
		t.Fatal("audit leaked content or lost detections")
	}
	line = verifyLogAudit(verifyLogArguments{run: "forged\nverified", stream: "forged\nverified", name: "secret"}, r)
	if strings.Contains(line, "forged") || strings.Contains(line, "secret") || strings.Count(line, "\n") != 1 {
		t.Fatal("audit injection")
	}
}
