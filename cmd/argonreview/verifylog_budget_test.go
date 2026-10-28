package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyLogStreamBudgets(t *testing.T) {
	for _, row := range []struct {
		stream, name string
		limit        int
	}{
		{"index", "", verifyLogIndexReads}, {"test", "#1", verifyLogEntryReads},
		{"package", "#2", verifyLogEntryReads}, {"stderr", "", verifyLogAuxReads}, {"output", "", verifyLogAuxReads},
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
			// A spent stream does not consume another stream's sub-budget.
			other := "index"
			if row.stream == other {
				other = "stderr"
			}
			if verifyLogRead(t, s, c, other, "", "1").class != verifyLogOK {
				t.Fatal("stream budgets are coupled")
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
	s := &verifyLogStore{}
	c := s.capture("FAILED", verifyLogFixture("evidence"))
	for range verifyLogEntryReads {
		if verifyLogRead(t, s, c, "test", "absent", "1").class != verifyLogNotListed {
			t.Fatal("unexpected refusal")
		}
	}
	if verifyLogRead(t, s, c, "test", "#1", "1").class != verifyLogBudget {
		t.Fatal("refusals did not consume per-capture budget")
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
