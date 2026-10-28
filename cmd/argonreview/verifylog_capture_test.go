package main

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func verifyLogFixture(text string) *containerRecord {
	e := &aggregatorEntry{kind: "test", pkg: "p", name: "TestBad", state: "fail", output: retainedOutput{head: []byte(text), total: len(text)}}
	return &containerRecord{sealed: true, complete: true, entries: []*aggregatorEntry{e}}
}

func TestVerifyLogCapture(t *testing.T) {
	r := verifyLogFixture("output")
	for _, first := range []string{"FAILED", "FAILED: cleanup failed", "NOT VERIFIED: time limit exceeded"} {
		c := newVerifyLogCapture(first, r)
		if c == nil || c.record != r || &c.record.entries[0].output.head[0] != &r.entries[0].output.head[0] {
			t.Fatal("capture must pin the sealed record by reference")
		}
	}
	for _, first := range []string{"verified", "verified\n1 passing leaf tests", "", "unknown"} {
		if newVerifyLogCapture(first, r) != nil {
			t.Fatalf("captured %q", first)
		}
	}
	if newVerifyLogCapture("FAILED", nil) != nil || newVerifyLogCapture("FAILED", &containerRecord{}) != nil {
		t.Fatal("captured an unsealed record")
	}
}

func TestVerifyLogRecordMax(t *testing.T) {
	want := aggregatorOutputLimit + aggregatorStderrLimit + aggregatorNameLimit + aggregatorStdoutLimit + aggregatorBaseCharge + aggregatorBrowserCharge
	if verifyLogRecordMax != want || verifyLogCaptureMax != want+verifyLogDerivedAllowance || verifyLogStoreBytes != verifyLogCaptures*verifyLogCaptureMax {
		t.Fatal("record/store limit is not derived from aggregation plus the named allowance")
	}
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			r := verifyLogFixture("")
			base := newVerifyLogCapture("FAILED", r).size
			// Deliberately synthetic: even an above-envelope capture must retain
			// the newest failure rather than silently discard it.
			r.stdout.head = make([]byte, verifyLogCaptureMax-base+delta)
			s := &verifyLogStore{}
			c := s.capture("FAILED", r)
			if c == nil || c.size != verifyLogCaptureMax+delta || s.bytes != c.size {
				t.Fatal("newest capture refused or pinned bytes not counted")
			}
		})
	}
}

func TestVerifyLogPinnedBytes(t *testing.T) {
	r := verifyLogFixture("head")
	c := newVerifyLogCapture("FAILED", r)
	base := c.pinnedBytes()
	r.entries[0].output.tail = []byte("tail")
	if c.pinnedBytes()-base != len("tail") {
		t.Fatal("tail was not charged")
	}
	base = c.pinnedBytes()
	r.browser = &browserRecord{importPath: "p", run: "TestBrowser", namedState: "fail", retained: 1}
	r.browser.entries[0] = browserLeaf{"TestBrowser/sub", "fail"}
	if c.pinnedBytes()-base != aggregatorBrowserCharge+textCharge(len("p"))+textCharge(len("TestBrowser"))+textCharge(len("TestBrowser/sub")) {
		t.Fatal("browser evidence was not charged")
	}
	base = c.pinnedBytes()
	c.entries[0].display += strings.Repeat("x", aggregatorAllocationQuantum)
	if c.pinnedBytes()-base != aggregatorAllocationQuantum {
		t.Fatal("derived names were not charged")
	}
}

func TestVerifyLogStore(t *testing.T) {
	s := &verifyLogStore{}
	var ids []string
	for i := 0; i <= verifyLogCaptures; i++ {
		c := s.capture("FAILED", verifyLogFixture("evidence"))
		if c == nil {
			t.Fatal("no capture")
		}
		b, err := hex.DecodeString(c.id)
		if err != nil || len(b) != verifyLogIDBytes {
			t.Fatal("invalid run ID")
		}
		for _, id := range ids {
			if c.id == id {
				t.Fatal("run ID reused")
			}
		}
		ids = append(ids, c.id)
	}
	if len(s.captures) != verifyLogCaptures || s.find(ids[0]) != nil || s.find(ids[1]) == nil {
		t.Fatal("eviction is not oldest first")
	}
	sum := 0
	for _, c := range s.captures {
		sum += c.size
	}
	if s.bytes != sum {
		t.Fatal("eviction lost byte accounting")
	}
	if s.capture("verified", verifyLogFixture("ignored")) != nil || s.bytes != sum {
		t.Fatal("verified result changed store")
	}
}

func TestVerifyLogStoreByteEviction(t *testing.T) {
	s := &verifyLogStore{}
	old := s.capture("FAILED", verifyLogFixture("old"))
	nextSize := newVerifyLogCapture("FAILED", verifyLogFixture("new")).size
	// The count and record-size caps imply the byte cap today. Seed accounting
	// at its boundary to test the independent defence against future changes.
	for _, delta := range []int{0, 1} {
		s.captures = []*verifyLogCapture{old}
		old.size = verifyLogStoreBytes - nextSize + delta
		s.bytes = old.size
		next := s.capture("FAILED", verifyLogFixture("new"))
		if next == nil || (s.find(old.id) == nil) != (delta > 0) {
			t.Fatal("byte eviction boundary changed")
		}
		if s.bytes > verifyLogStoreBytes {
			t.Fatal("store exceeded byte cap")
		}
	}
	// Stay below the count cap while distinguishing oldest from newest eviction.
	middle := newVerifyLogCapture("FAILED", verifyLogFixture("middle"))
	middle.id = "middle"
	old.size = verifyLogStoreBytes - nextSize - middle.size + 1
	s.captures, s.bytes = []*verifyLogCapture{old, middle}, old.size+middle.size
	next := s.capture("FAILED", verifyLogFixture("new"))
	if next == nil || s.find(next.id) != next || s.find(old.id) != nil || s.find(middle.id) != middle || len(s.captures) != 2 || s.bytes != middle.size+next.size {
		t.Fatal("byte eviction must remove oldest first and retain newest")
	}
}

func TestVerifyLogIndex(t *testing.T) {
	r := verifyLogFixture("")
	r.entries = []*aggregatorEntry{
		{kind: "package", pkg: "z", state: "fail"},
		{kind: "test", pkg: "p", name: "Z", state: "fail"},
		{kind: "test", pkg: "p", name: "A", state: "skip"},
		{kind: "test", pkg: "p", name: "Pass", state: "pass"},
		{kind: "test", pkg: "p", name: "Allowed", state: "skip", allowed: true},
		{kind: "test", pkg: "p", name: "Parent", state: "fail", parent: true},
		{kind: "package", pkg: "skip", state: "skip"},
		{kind: "package", pkg: "a", state: "fail", buildFailed: true},
	}
	c := newVerifyLogCapture("FAILED", r)
	want := []string{"p/A", "p/Z", "z: package failed", "a [build failed]"}
	if len(c.entries) != len(want) {
		t.Fatalf("index membership: %+v", c.entries)
	}
	for i, e := range c.entries {
		if e.display != want[i] {
			t.Fatalf("ordinal #%d = %q", i+1, e.display)
		}
	}
}

func TestVerifyLogNames(t *testing.T) {
	for _, raw := range []string{strings.Repeat("x", summaryIdentityLimit), "A\x00B\u202eC", "bad\xffname"} {
		t.Run(fmt.Sprintf("%x", []byte(raw[:min(len(raw), 8)])), func(t *testing.T) {
			r := verifyLogFixture("")
			r.entries[0].name = raw
			c := newVerifyLogCapture("FAILED", r)
			if c.entries[0].display != summaryText("p/"+raw) {
				t.Fatal("display differs from summaryText")
			}
		})
	}
	r := verifyLogFixture("")
	r.entries = append(r.entries, &aggregatorEntry{kind: "test", pkg: "p", name: "Test\x00Bad", state: "fail"})
	c := newVerifyLogCapture("FAILED", r)
	if !c.entries[0].ambiguous || !c.entries[1].ambiguous {
		t.Fatal("collision not marked on every entry")
	}
	if strings.Count(strings.Join(c.indexLines(), "\n"), "ambiguous: use #N") != len(c.entries) {
		t.Fatal("ambiguous index notice missing")
	}
}

func TestVerifyLogTaint(t *testing.T) {
	_, database, _ := strings.Cut(containerTestDatabaseURL, "=")
	u, err := url.Parse(database)
	if err != nil || u.User == nil {
		t.Fatal("CI database fixture has no user information")
	}
	password, ok := u.User.Password()
	if !ok || password == "" || verifyLogScan("diagnostic "+password) == 0 {
		t.Fatal("CI credential tripwire failed")
	}
	for _, text := range []string{"ghp_placeholder", "github_pat_placeholder", "AKIAplaceholder", "-----BEGIN PRIVATE KEY-----\nbody\n-----END PRIVATE KEY-----", "gh\np_placeholder", "g\x1b[31mhp_placeholder", "gh\u200bp_placeholder"} {
		r := verifyLogFixture(text)
		c := newVerifyLogCapture("FAILED", r)
		if c.taint[&r.entries[0].output] == 0 {
			t.Fatalf("taint not detected for %q", text)
		}
	}
	r := verifyLogFixture("safe")
	r.entries[0].name = "Test/ghp_placeholder"
	c := newVerifyLogCapture("FAILED", r)
	if c.entries[0].display != "[name withheld]" || !c.entries[0].withheld || c.nameDetections != 1 {
		t.Fatal("secret-shaped name exposed")
	}
}

func TestVerifyLogAllRetainedNamesScanned(t *testing.T) {
	for _, row := range []struct {
		name, state string
		allowed     bool
	}{
		{strings.Repeat("x", summaryLineLimit) + "ghp_placeholder", "fail", false},
		{"Test/gh\u200bp_placeholder", "fail", false},
		{"Test/ghp_placeholder", "skip", true},
	} {
		r := verifyLogFixture("safe")
		r.entries[0].name, r.entries[0].state, r.entries[0].allowed = row.name, row.state, row.allowed
		c := newVerifyLogCapture("FAILED", r)
		if c.nameDetections != 1 {
			t.Fatal("retained name scan was capped or filtered by membership")
		}
		if !row.allowed && c.entries[0].display != "[name withheld]" {
			t.Fatal("disguised name exposed")
		}
	}
}
