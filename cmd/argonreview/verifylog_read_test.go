package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func verifyLogRead(t *testing.T, s *verifyLogStore, c *verifyLogCapture, stream, name, start string) verifyLogReply {
	t.Helper()
	r, audit := s.readRaw(verifyLogArgsJSON(c.id, stream, name, start), verifyLogMaxID())
	verifyLogAssertWire(t, r)
	if !strings.Contains(audit, fmt.Sprintf("bytes=%d class=%s", len(r.line), r.class)) {
		t.Fatal("audit does not describe the response")
	}
	return r
}

func TestVerifyLogReadSelectors(t *testing.T) {
	for _, row := range []struct {
		stream, name string
		class        verifyLogClass
	}{
		{"index", "", verifyLogOK}, {"test", "#1", verifyLogOK}, {"test", "p/TestBad", verifyLogOK},
		{"test", "p/Passing", verifyLogNotListed}, {"test", "../path", verifyLogNotListed},
		{"test", "#2", verifyLogNotListed}, {"package", "#1", verifyLogNotListed},
		{"package", "p/TestBad", verifyLogNotListed},
		{"output", "", verifyLogStreamDenied}, {"stderr", "", verifyLogOK},
	} {
		t.Run(row.stream+"/"+row.name, func(t *testing.T) {
			s := &verifyLogStore{}
			c := s.capture("FAILED", verifyLogFixture("retained evidence"))
			r := verifyLogRead(t, s, c, row.stream, row.name, "1")
			if r.class != row.class {
				t.Fatalf("class %s, want %s", r.class, row.class)
			}
		})
	}
	for _, start := range []string{"10", "1234567"} {
		s := &verifyLogStore{}
		c := s.capture("FAILED", verifyLogFixture("one line"))
		if verifyLogRead(t, s, c, "test", "#1", start).class != verifyLogRange {
			t.Fatal("reader must check the line count")
		}
	}
	s := &verifyLogStore{}
	c := s.capture("FAILED", verifyLogFixture("evidence"))
	reply, _ := s.read(verifyLogArguments{run: c.id, stream: "unknown", start: 1}, json.RawMessage(`1`), verifyLogOK)
	if reply.class != verifyLogStreamDenied {
		t.Fatal("direct reader admitted an unknown stream")
	}
}

func TestVerifyLogReadNoRun(t *testing.T) {
	s := &verifyLogStore{}
	old := s.capture("FAILED", verifyLogFixture("old"))
	for i := 0; i < verifyLogCaptures; i++ {
		s.capture("FAILED", verifyLogFixture("new"))
	}
	other := (&verifyLogStore{}).capture("FAILED", verifyLogFixture("other worker"))
	for _, run := range []string{old.id, other.id, strings.Repeat("0", verifyLogIDBytes*2), "malformed", ""} {
		r, _ := s.read(verifyLogArguments{run: run, stream: "index", start: 1}, json.RawMessage(`1`), verifyLogOK)
		if r.class != verifyLogNoRun || verifyLogReplyText(r) != verifyLogRefusals()[verifyLogNoRun] {
			t.Fatal("run existence oracle")
		}
	}
	if s.capture("verified", verifyLogFixture("verified")) != nil {
		t.Fatal("verified run was stored")
	}
}

func TestVerifyLogReadNames(t *testing.T) {
	s := &verifyLogStore{}
	r := verifyLogFixture("selected by ordinal")
	r.entries = append(r.entries, &aggregatorEntry{kind: "test", pkg: "p", name: "Test\x00Bad", state: "fail"})
	c := s.capture("FAILED", r)
	if verifyLogRead(t, s, c, "test", "p/TestBad", "1").class != verifyLogAmbiguous {
		t.Fatal("ambiguous name admitted")
	}
	if verifyLogRead(t, s, c, "test", "#1", "1").class != verifyLogOK {
		t.Fatal("ambiguous ordinal refused")
	}
	// An ordinal-looking display is never a second lookup interpretation.
	c.entries[0].display = "#2"
	e, class := c.selectEntry(verifyLogArguments{stream: "test", name: "#2"})
	if class != verifyLogOK || e != c.entries[1].entry {
		t.Fatal("ordinal did not take precedence")
	}
	r = verifyLogFixture("safe output")
	r.entries[0].name = "Test/ghp_placeholder"
	c = s.capture("FAILED", r)
	index := verifyLogRead(t, s, c, "index", "", "1")
	if strings.Contains(verifyLogReplyText(index), "ghp_") || !strings.Contains(verifyLogReplyText(index), "[name withheld]") {
		t.Fatal("secret-shaped name exposed")
	}
	if verifyLogRead(t, s, c, "test", "#1", "1").class != verifyLogOK || verifyLogRead(t, s, c, "test", "[name withheld]", "1").class != verifyLogNotListed {
		t.Fatal("withheld name must remain ordinal-only")
	}
}

func TestVerifyLogReadHostileNames(t *testing.T) {
	for _, name := range []string{strings.Repeat("x", summaryIdentityLimit), "Test\x00\u202eName", "Test\xffName"} {
		s := &verifyLogStore{}
		r := verifyLogFixture("ordinal evidence")
		r.entries[0].name = name
		c := s.capture("FAILED", r)
		if verifyLogRead(t, s, c, "test", "#1", "1").class != verifyLogOK {
			t.Fatal("hostile name broke its ordinal")
		}
		if verifyLogRead(t, s, c, "test", c.entries[0].display, "1").class != verifyLogOK {
			t.Fatal("safe display name could not be selected")
		}
		rawName, _ := json.Marshal("p/" + name)
		if strings.Contains(name, "\xff") {
			rawName = []byte(`"p/Test` + "\xff" + `Name"`)
		}
		raw := strings.Replace(string(verifyLogArgsJSON(c.id, "test", "PLACEHOLDER", "1")), `"PLACEHOLDER"`, string(rawName), 1)
		reply, _ := s.readRaw([]byte(raw), json.RawMessage(`1`))
		if reply.class != verifyLogMalformed {
			t.Fatal("raw hostile name crossed the syntax boundary")
		}
	}
}

func TestVerifyLogReadTaintedWholeUnit(t *testing.T) {
	for _, stream := range []string{"test", "package", "stderr", "output"} {
		for _, start := range []string{"1", "2", "3", "10"} {
			t.Run(stream+"/"+start, func(t *testing.T) {
				s := &verifyLogStore{}
				r := verifyLogFixture("safe")
				r.command = true
				secret := retainedOutput{head: []byte("-----BEGIN PRIVATE KEY-----\nbody\nend\n")}
				names := ""
				switch stream {
				case "test":
					r.entries[0].output = secret
					names = "#1"
				case "package":
					r.entries[0].kind = "package"
					r.entries[0].name = ""
					r.packageOutput = secret
					names = "#1"
				case "stderr":
					r.stderr = secret
				case "output":
					r.stdout = secret
				}
				c := s.capture("FAILED", r)
				reply := verifyLogRead(t, s, c, stream, names, start)
				if reply.class != verifyLogTainted || reply.detections == 0 || strings.Contains(verifyLogReplyText(reply), "body") {
					t.Fatal("taint bypass by start")
				}
			})
		}
	}
}

func TestVerifyLogReadStderrRunnerFailure(t *testing.T) {
	for _, code := range []int{125, 126, 127} {
		a := aggregatorFixture(containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"))
		_, _ = a.stderrWriter().Write([]byte("host path must never be served"))
		a.finish(code, nil, aggregatorRunState{completed: true})
		s := &verifyLogStore{}
		c := s.capture("FAILED", a.record)
		reply := verifyLogRead(t, s, c, "stderr", "", "1")
		if reply.class != verifyLogStderrDenied || strings.Contains(verifyLogReplyText(reply), "host path") {
			t.Fatalf("exit %d exposed runner stderr", code)
		}
	}
}

func TestVerifyLogReadPackage(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"))
	aggregatorWrite(a, `{"Action":"build-output","ImportPath":"q","Output":"compiler evidence\n"}`+"\n"+`{"Action":"build-fail","ImportPath":"q"}`+"\n")
	a.record.packageOutput = retainedOutput{head: []byte("package evidence\n"), total: len("package evidence\n")}
	a.finish(1, nil, aggregatorRunState{completed: true})
	s := &verifyLogStore{}
	c := s.capture("FAILED", a.record)
	for i, item := range c.entries {
		if item.entry.kind != "package" {
			continue
		}
		reply := verifyLogRead(t, s, c, "package", fmt.Sprintf("#%d", i+1), "1")
		text := verifyLogReplyText(reply)
		if reply.class != verifyLogOK || !strings.Contains(text, "compiler evidence") || !strings.Contains(text, "package evidence") {
			t.Fatal("shared package diagnostics missing")
		}
	}
}

func TestVerifyLogReadPrefix(t *testing.T) {
	for _, stream := range []string{"stderr", "output"} {
		for _, row := range []struct {
			name      string
			headBytes int
			tail      string
			dropped   int
			unserved  int
		}{
			{"over-prefix", verifyLogPrefixBytes + len("outside prefix"), "", len("omitted"), len("outside prefix")},
			{"at-prefix", verifyLogPrefixBytes, "", len("omitted"), 0},
			{"below-prefix", verifyLogPrefixBytes - 1, "", len("omitted"), 0},
			{"with-tail", verifyLogPrefixBytes + len("outside prefix"), "retained tail\n", len("omitted"), len("outside prefix") + len("retained tail\n")},
			{"short-head-with-tail", verifyLogPrefixBytes - 1, "retained tail\n", len("omitted"), len("retained tail\n")},
		} {
			t.Run(stream+"/"+row.name, func(t *testing.T) {
				s := &verifyLogStore{}
				r := verifyLogFixture("")
				r.command = true
				o := &r.stderr
				if stream == "output" {
					o = &r.stdout
				}
				o.head, o.tail = []byte(strings.Repeat("x", row.headBytes)), []byte(row.tail)
				o.total = len(o.head) + len(o.tail) + row.dropped
				o.notRetained = true
				c := s.capture("FAILED", r)
				lines, _, class := c.unit(verifyLogArguments{stream: stream})
				want := []string{strings.Repeat("x", min(row.headBytes, verifyLogPrefixBytes)), fmt.Sprintf("truncated: %d bytes not retained", row.dropped), "not retained"}
				if row.unserved > 0 {
					want = append(want, fmt.Sprintf("first 16 KiB served; %d retained bytes not served", row.unserved))
				}
				if class != verifyLogOK || !slices.Equal(lines, want) {
					t.Fatalf("prefix lines = %q, want %q (class %s)", lines, want, class)
				}
			})
		}
		s := &verifyLogStore{}
		r := verifyLogFixture("")
		r.command = true
		o := &r.stderr
		if stream == "output" {
			o = &r.stdout
		}
		o.head = []byte(strings.Repeat("x", verifyLogPrefixBytes) + "outside prefix")
		o.total = len(o.head)
		c := s.capture("FAILED", r)
		lines, _, class := c.unit(verifyLogArguments{stream: stream})
		if class != verifyLogOK || len(lines[0]) != verifyLogPrefixBytes || strings.Contains(strings.Join(lines, "\n"), "outside prefix") {
			t.Fatal("auxiliary prefix boundary")
		}
		if !strings.Contains(strings.Join(lines, "\n"), fmt.Sprintf("first 16 KiB served; %d retained bytes not served", len("outside prefix"))) || strings.Contains(strings.Join(lines, "\n"), "bytes not retained") {
			t.Fatal("prefix omission confused with record loss")
		}
		// Detection covers the retained unit even beyond the served prefix.
		o.head = append(o.head, []byte("ghp_placeholder")...)
		c = s.capture("FAILED", r)
		if verifyLogRead(t, s, c, stream, "", "1").class != verifyLogTainted {
			t.Fatal("scan was capped at served prefix")
		}
	}
}

func TestVerifyLogRefusals(t *testing.T) {
	seen := make(map[string]bool)
	for class, message := range verifyLogRefusals() {
		t.Run(string(class), func(t *testing.T) {
			r := verifyLogRefuse(verifyLogMaxID(), class)
			verifyLogAssertWire(t, r)
			if message == "" || seen[message] || verifyLogReplyText(r) != message {
				t.Fatal("refusal table is not fixed and distinct")
			}
			seen[message] = true
			if class == verifyLogMalformed && (r.response.Error == nil || r.response.Error.Code != -32602) {
				t.Fatal("malformed arguments must use the protocol error")
			}
		})
	}
	s := &verifyLogStore{}
	r, _ := s.readRaw([]byte(`{"name":"never echo this"}`), json.RawMessage(`1`))
	if r.class != verifyLogMalformed || strings.Contains(string(r.line), "never echo") {
		t.Fatal("malformed input echoed")
	}
	s.reads, s.served = verifyLogServerReads, verifyLogServerBytes
	r, _ = s.readRaw([]byte(`null`), json.RawMessage(`1`))
	if r.class != verifyLogMalformed || r.response.Error == nil {
		t.Fatal("spent budget changed a protocol error")
	}
	c := s.capture("FAILED", verifyLogFixture("safe"))
	c.reads[verifyLogBudgetGroup("test")] = verifyLogEntryReads
	r, _ = s.readRaw(verifyLogArgsJSON(c.id, "test", "#0", "1"), json.RawMessage(`1`))
	if r.class != verifyLogMalformed {
		t.Fatal("capture budget changed a protocol error")
	}
	r, _ = s.readRaw(verifyLogArgsJSON(c.id, "index", "", "1"), json.RawMessage(`true`))
	if r.class != verifyLogMalformed || string(r.response.ID) != "null" {
		t.Fatal("invalid RPC ID was not normalized")
	}
}

func TestVerifyLogReadConcurrent(t *testing.T) {
	s := &verifyLogStore{}
	r := verifyLogFixture("read while another run is in flight")
	first := s.capture("FAILED", r)
	var wg sync.WaitGroup
	for range verifyLogCaptures {
		wg.Go(func() {
			for range verifyLogCaptures {
				s.capture("FAILED", r)
			}
		})
		wg.Go(func() {
			for range verifyLogIndexReads {
				_, _ = s.readRaw(verifyLogArgsJSON(first.id, "index", "", "1"), json.RawMessage(`1`))
			}
		})
	}
	wg.Wait()
	if len(s.captures) != verifyLogCaptures {
		t.Fatal("concurrent eviction")
	}
}
