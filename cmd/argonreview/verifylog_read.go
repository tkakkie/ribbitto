package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func verifyLogOutputLines(o *retainedOutput, prefix bool) []string {
	var lines []string
	head, tail := o.head, o.tail
	if prefix {
		head = head[:min(len(head), verifyLogPrefixBytes)]
		tail = nil
	}
	for _, part := range [][]byte{head, tail} {
		if len(part) > 0 {
			lines = append(lines, strings.Split(strings.TrimSuffix(string(part), "\n"), "\n")...)
		}
	}
	if dropped := o.total - len(o.head) - len(o.tail); dropped > 0 {
		lines = append(lines, fmt.Sprintf("truncated: %d bytes not retained", dropped))
	}
	if o.notRetained {
		lines = append(lines, "not retained")
	}
	if unserved := len(o.head) + len(o.tail) - len(head) - len(tail); unserved > 0 {
		lines = append(lines, fmt.Sprintf("first 16 KiB served; %d retained bytes not served", unserved))
	}
	if len(lines) == 0 {
		lines = []string{"(no retained output)"}
	}
	return lines
}

func (c *verifyLogCapture) selectEntry(a verifyLogArguments) (*aggregatorEntry, verifyLogClass) {
	if strings.HasPrefix(a.name, "#") {
		n, err := strconv.Atoi(a.name[1:])
		if err == nil && verifyLogDigits(a.name[1:], verifyLogOrdinalDigits) && n <= len(c.entries) {
			e := c.entries[n-1].entry
			if e.kind == a.stream {
				return e, verifyLogOK
			}
		}
		return nil, verifyLogNotListed
	}
	for _, item := range c.entries {
		if item.display != a.name {
			continue
		}
		if item.ambiguous {
			return nil, verifyLogAmbiguous
		}
		// Withheld identities stay ordinal-only, including a lone withheld name.
		if item.withheld || item.entry.kind != a.stream {
			return nil, verifyLogNotListed
		}
		return item.entry, verifyLogOK
	}
	return nil, verifyLogNotListed
}

func (c *verifyLogCapture) unit(a verifyLogArguments) ([]string, int, verifyLogClass) {
	r := c.record
	switch a.stream {
	case "index":
		return c.indexLines(), 0, verifyLogOK
	case "test", "package":
		e, class := c.selectEntry(a)
		if class != verifyLogOK {
			return nil, 0, class
		}
		if a.stream == "test" {
			return verifyLogOutputLines(&e.output, false), c.taint[&e.output], verifyLogOK
		}
		// The aggregator stores package and compiler diagnostics in shared run
		// buffers, not in package-entry output. Serve both for each failed package
		// rather than inventing package attribution that the record does not keep.
		lines := append(verifyLogOutputLines(&r.packageOutput, false), verifyLogOutputLines(&r.compiler, false)...)
		return lines, c.packageDetections, verifyLogOK
	case "stderr":
		if r.stderrNotServable {
			return nil, 0, verifyLogStderrDenied
		}
		return verifyLogOutputLines(&r.stderr, true), c.taint[&r.stderr], verifyLogOK
	case "output":
		if !r.command {
			return nil, 0, verifyLogStreamDenied
		}
		return verifyLogOutputLines(&r.stdout, true), c.taint[&r.stdout], verifyLogOK
	}
	return nil, 0, verifyLogStreamDenied
}

func verifyLogReadLimit(stream string) int {
	switch stream {
	case "index":
		return verifyLogIndexReads
	case "test", "package":
		return verifyLogEntryReads
	default:
		return verifyLogAuxReads
	}
}

func verifyLogBudgetGroup(stream string) int {
	switch stream {
	case "index":
		return 0
	case "test", "package":
		return 1
	case "stderr", "output":
		return 2
	default:
		return -1
	}
}

// readRaw returns exactly the encoded line to write. Budgets count that line,
// including refused requests and the protocol envelope, never captured bytes.
// The server loop writes it unchanged and emits its one audit line.
func (s *verifyLogStore) readRaw(raw []byte, id json.RawMessage) (verifyLogReply, string) {
	a, err := parseVerifyLogArguments(raw)
	class := verifyLogOK
	if err != nil {
		class = verifyLogMalformed
	}
	return s.read(a, id, class)
}

func (s *verifyLogStore) read(a verifyLogArguments, id json.RawMessage, class verifyLogClass) (verifyLogReply, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validVerifyID(id) {
		id = json.RawMessage("null")
		class = verifyLogMalformed
	}
	c := s.find(a.run)
	group := verifyLogBudgetGroup(a.stream)
	if class == verifyLogOK && (!verifyLogRunID(a.run) || c == nil) {
		class = verifyLogNoRun
	}
	if class != verifyLogMalformed && (s.reads >= verifyLogServerReads || s.served >= verifyLogServerBytes) {
		class = verifyLogBudget
	}
	if c != nil && group >= 0 {
		if class != verifyLogMalformed && c.reads[group] >= verifyLogReadLimit(a.stream) {
			class = verifyLogBudget
		}
		c.reads[group]++
	}
	s.reads++
	detections, names := 0, 0
	var reply verifyLogReply
	if class == verifyLogOK {
		var lines []string
		lines, detections, class = c.unit(a)
		names = c.nameDetections
		if detections > 0 {
			class = verifyLogTainted
		}
		if class == verifyLogOK {
			reply = verifyLogPage(id, lines, a.start)
		}
	}
	if class != verifyLogOK {
		reply = verifyLogRefuse(id, class)
	}
	reply = verifyLogBound(reply)
	if reply.class != verifyLogMalformed && s.served+len(reply.line) > verifyLogServerBytes {
		reply = verifyLogRefuse(id, verifyLogBudget)
	}
	// Mandatory fixed refusals still have a wire cost after exhaustion. Counting
	// them can exceed the ceiling; no further successful page is then admitted.
	s.served += len(reply.line)
	reply.detections, reply.nameDetections = detections, names
	return reply, verifyLogAudit(a, reply)
}
