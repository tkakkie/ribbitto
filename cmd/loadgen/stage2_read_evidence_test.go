package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestStage2StreamEligibility(t *testing.T) {
	d := stage2EvidenceDriver(t, 1000)
	topic := d.data.Stage2.NamedTopics[0].ID
	m := d.members[0]
	for _, tc := range []struct {
		name, scope               string
		seq, applied, feed, topic uint64
		want                      bool
	}{
		{"other post", "feed", 81, 90, 80, 80, true},
		{"equal feed bound", "feed", 81, 90, 81, 0, false},
		{"equal topic bound", "feed", 81, 90, 0, 81, false},
		{"not applied", "feed", 91, 90, 0, 0, false},
		{"topic covered", topic, 81, 90, 0, 80, true},
		{"feed covers topic", topic, 81, 90, 81, 0, false},
		{"wrong topic", d.data.Stage2.NamedTopics[1].ID, 81, 90, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.other[topic], m.applied, m.used["feed"], m.used[topic] = tc.seq, tc.applied, tc.feed, tc.topic
			cursor, ok := m.eligible(tc.scope)
			if ok != tc.want || (ok && cursor != tc.applied) {
				t.Fatalf("stream eligibility: cursor=%d eligible=%v want=%v", cursor, ok, tc.want)
			}
		})
	}
	clear(m.used)
	clear(m.other)
	m.applied = 0
	d.applyFrame(0, 100, stage2PostMarker(stage2Slot{Endpoint: "feed", LogicalID: 0}))
	if _, ok := m.eligible("feed"); ok || m.applied != 100 {
		t.Fatal("own post became unread evidence or lost applied cursor")
	}
	for _, marker := range []string{"", "stage2-post:feed:bad", "stage2-post:other:1", "stage2-post:feed:1:extra"} {
		d.applyFrame(0, 101, marker)
	}
	if _, ok := m.eligible("feed"); ok {
		t.Fatal("unmarked frame became unread evidence")
	}
	d.applyFrame(0, 0, stage2PostMarker(stage2Slot{Endpoint: "feed", LogicalID: 1}))
	d.applyFrame(0, 1<<63, "")
	if m.applied != 101 {
		t.Fatal("invalid durable sequence advanced cursor")
	}
	d.applyFrame(0, 102, stage2PostMarker(stage2Slot{Endpoint: "topic", LogicalID: 1}))
	d.applyFrame(0, 99, stage2PostMarker(stage2Slot{Endpoint: "topic", LogicalID: 1}))
	if c, ok := m.eligible("feed"); !ok || c != 102 || m.other[topic] != 102 {
		t.Fatal("applied other-topic post lost or cursor regressed")
	}
	d.applyFrame(1, 103, stage2PostMarker(stage2Slot{Endpoint: "topic", LogicalID: 2}))
	if _, ok := d.members[1].eligible(topic); ok {
		t.Fatal("another scope became topic evidence")
	}
	m.reserve("feed", 102)
	if _, ok := m.eligible("feed"); ok {
		t.Fatal("reserved stream evidence was reused")
	}
}

func TestStage2ReadSelection(t *testing.T) {
	d := stage2EvidenceDriver(t, 1000)
	for _, i := range []int{0, 2, 998} {
		d.members[i].applyPage("feed", stage2ReadPage{80, true})
	}
	d.members[999].applyPage("feed", stage2ReadPage{80, true})
	// Starting at 998, skip its busy admission, then wrap past wrong scope 999.
	d.members[998].admission.Lock()
	d.members[0].mu.Lock() // Evidence intake busy: selection must skip, never wait.
	member, cursor, ok := d.selectRead(stage2Slot{Endpoint: "feed", LogicalID: 998})
	d.members[0].mu.Unlock()
	d.members[998].admission.Unlock()
	if !ok || member != 2 || cursor != 80 {
		t.Fatalf("rotation did not skip busy members and wrap: %d %d %v", member, cursor, ok)
	}
	if d.members[member].mu.TryLock() {
		t.Fatal("selection did not retain evidence through reservation")
	}
	d.members[member].mu.Unlock()
	d.members[member].admission.Unlock()
	var visited []uint64
	if _, ok := selectStage2Member(998, 1000, func(i uint64) bool { visited = append(visited, i); return false }); ok || len(visited) != 1000 || visited[0] != 998 || visited[999] != 997 {
		t.Fatal("refusal scan is not bounded to N in rotation order")
	}
	if len(slices.Compact(slices.Sorted(slices.Values(visited)))) != 1000 {
		t.Fatal("refusal scan visited a member more than once")
	}
}

func TestStage2SelectedReadHTTP(t *testing.T) {
	d := stage2EvidenceDriver(t, 1000)
	const id = uint64(7) // Scheduled topic 2; its member is busy, next match is 11.
	slot := stage2Slot{Operation: "read", Endpoint: "topic", LogicalID: id, At: time.Unix(0, 0)}
	scope := d.readScope(slot)
	d.members[7].applyPage(scope, stage2ReadPage{90, true})
	d.members[7].admission.Lock()
	defer d.members[7].admission.Unlock()
	d.members[11].applyPage(scope, stage2ReadPage{80, true})
	d.applyFrame(11, 90, stage2PostMarker(stage2Slot{Endpoint: "topic", LogicalID: 6}))
	calls, status := 0, 204
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cookie, err := r.Cookie("__Host-session")
		if err != nil || cookie.Value != "session-11" || r.URL.Path != "/organizations/paper-lantern/channels/"+d.data.Stage2.HotChannelID+"/topics/"+scope+"/read" || r.FormValue("cursor") != "90" {
			t.Error("selected member, scheduled topic or newest applied read cursor changed")
		}
		// Frame intake must work while this member's HTTP admission is held.
		d.applyFrame(11, 91, "")
		w.WriteHeader(status)
	}))
	defer server.Close()
	for i := range d.data.Accounts {
		d.data.Accounts[i].Tokens = []string{fmt.Sprintf("session-%d", i)}
	}
	tr, err := newTransport(server.URL, "", &counts{})
	if err != nil {
		t.Fatal(err)
	}
	d.transport = tr
	defer tr.CloseIdleConnections()
	now := func() time.Time { return slot.At }
	d.verifyRead = func(_ context.Context, o stage2Observation, c uint64) string {
		if o.Slot != slot || !o.MemberSelected || o.Member != 11 || c != 90 {
			t.Error("verifier lost selected member")
		}
		return "unknown"
	}
	got, err := d.drive(t.Context(), stage2Observation{Slot: slot}, slot.At, now, "")
	if err != nil || got.Outcome != "unverified" || got.Member != 11 || !got.MemberSelected || len(got.Attempts) != 1 {
		t.Fatalf("selected read observation: %+v %v", got, err)
	}
	counts := stage2MemberReadCounts([]stage2Observation{got}, 1000)
	if counts[11] != 1 || counts[7] != 0 {
		t.Fatal("per-member reads counted scheduled member instead of selected member")
	}
	// Reserve a fresh event even on an ambiguous transport failure (cancelled request).
	d.applyFrame(11, 92, stage2PostMarker(stage2Slot{Endpoint: "topic", LogicalID: 6}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failed, err := d.drive(ctx, stage2Observation{Slot: slot}, slot.At, now, "")
	if err != nil || failed.Outcome != "failed" || len(failed.Attempts) != 1 || d.members[11].used[scope] != 92 || len(d.members[11].pages) != 0 {
		t.Fatalf("ambiguous read lost reservation: %+v %v", failed, err)
	}
	before := calls
	refused, err := d.drive(t.Context(), stage2Observation{Slot: slot}, slot.At, now, "")
	if err == nil || refused.Outcome != "failed" || refused.MemberSelected || len(refused.Attempts) != 0 || calls != before {
		t.Fatal("empty eligible pool did not retain refusal without HTTP")
	}
	if stage2MemberReadCounts([]stage2Observation{got, failed, refused}, 1000)[11] != 2 {
		t.Fatal("member read counts lost ambiguous attempt or counted refusal")
	}
}
