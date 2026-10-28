package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestVerifyLogReadFullRecord(t *testing.T) {
	s := &verifyLogStore{}
	c := s.capture("FAILED", verifyLogFullRecord(t))
	if c == nil || c.size <= verifyLogRecordMax || c.size > verifyLogCaptureMax {
		t.Fatal("full-envelope capture missing or incorrectly accounted")
	}
	index := verifyLogRead(t, s, c, "index", "", "1")
	if index.class != verifyLogOK || !strings.Contains(verifyLogReplyText(index), "#1 test ") {
		t.Fatal("full-envelope index could not be read")
	}
	for i, item := range c.entries {
		if item.entry.kind != "test" || len(item.entry.output.head) == 0 {
			continue
		}
		reply := verifyLogRead(t, s, c, "test", fmt.Sprintf("#%d", i+1), "1")
		want := verifyLogLine(strings.SplitN(string(item.entry.output.head), "\n", 2)[0])
		if reply.class != verifyLogOK || !strings.Contains(verifyLogReplyText(reply), want) {
			t.Fatal("full-envelope entry output could not be read")
		}
		return
	}
	t.Fatal("full-envelope fixture has no selectable retained entry output")
}
