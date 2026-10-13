package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStage2ReadDrivers(t *testing.T) {
	var cursor uint64 = 80
	unread, status, calls, completedCalls := true, 204, 0, 0
	var slot stage2Slot
	var serverMu sync.Mutex
	server := httptest.NewServer(http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverMu.Lock()
		defer serverMu.Unlock()
		calls++
		cookie, err := r.Cookie("__Host-session")
		if err != nil || cookie.Value != fmt.Sprintf("session-%d", slot.LogicalID%1000) {
			t.Error("read member rotation")
		}
		path := "/organizations/paper-lantern/channels/00000000-0000-0000-0000-000000000001"
		if slot.Endpoint == "topic" {
			path += fmt.Sprintf("/topics/00000000-0000-0000-0000-%012d", 21+(slot.LogicalID/2)%2)
		}
		if slot.Operation == "read" {
			path += "/read"
			if r.Method != "POST" || r.FormValue("cursor") != fmt.Sprint(cursor) || len(r.PostForm) != 1 || r.URL.RawQuery != "" || r.Header.Get("HX-Request") != "true" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Proto != "HTTP/1.1" || r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
				t.Error("read request shape or committed cursor")
			}
			w.WriteHeader(status)
		} else if slot.Operation == "page" {
			button := ""
			if unread {
				button = `<button type="submit">read</button>`
			}
			_, _ = fmt.Fprintf(w, `<form id="read-form"><input type="hidden" name="cursor" value="%d">%s</form>`, cursor, button)
		} else if slot.Endpoint == "topic" {
			w.WriteHeader(303)
		}
		if r.URL.Path != path {
			t.Errorf("read scope path: %s, want %s", r.URL.Path, path)
		}
	})))
	defer server.Close()
	d, err := newStage2Driver(server.URL, stage2CredentialPath(t, stage2CredentialJSON()), fixedStage2().Groups[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.transport.CloseIdleConnections()
	start := time.Unix(123, 0)
	drive := func(operation, endpoint string, id uint64) (stage2Observation, error) {
		slot = stage2Slot{Operation: operation, Endpoint: endpoint, LogicalID: id, At: start}
		got, err := d.drive(t.Context(), stage2Observation{Slot: slot}, start, func() time.Time { return start }, "post")
		serverMu.Lock()
		completedCalls = calls
		serverMu.Unlock()
		return got, err
	}
	page := func(endpoint string, id uint64) {
		t.Helper()
		// Isolate snapshot rules from pool assignment (covered by schedule tests).
		m, scope := d.readMember(stage2Slot{Endpoint: endpoint, LogicalID: id})
		m.streamScope = scope
		if got, err := drive("page", endpoint, id); err != nil || got.Outcome != "succeeded" {
			t.Fatalf("page: %+v %v", got, err)
		}
	}
	refuse := func(endpoint string, id uint64) {
		t.Helper()
		before := completedCalls
		if _, err := drive("read", endpoint, id); err == nil || completedCalls != before {
			t.Fatal("read dispatched without newer unread snapshot")
		}
	}
	for _, tc := range []struct {
		endpoint    string
		id          uint64
		proof, want string
		status      int
	}{
		{"feed", 0, "", "unverified", 204},
		{"topic", 1, "changed", "changed", 204},
		{"topic", 2, "noop", "noop", 204},
		{"feed", 1003, "unknown", "unverified", 204},
		{"topic", 1002, "changed", "failed", 200},
		{"feed", 4, "changed", "failed", 503},
	} {
		t.Run(fmt.Sprintf("%s/%d/%s", tc.endpoint, tc.id, tc.want), func(t *testing.T) {
			cursor++
			page(tc.endpoint, tc.id)
			status, d.verifyRead = tc.status, nil
			verified := false
			if tc.proof != "" {
				d.verifyRead = func(_ context.Context, s stage2Observation, c uint64) string {
					verified = true
					if s.Slot != slot || !s.MemberSelected || s.Member != slot.LogicalID%1000 || c != cursor {
						t.Error("verifier scope/cursor")
					}
					return tc.proof
				}
			}
			before := completedCalls
			got, err := drive("read", tc.endpoint, tc.id)
			if err != nil || got.Outcome != tc.want || len(got.Attempts) != 1 || got.Attempts[0].Outcome != tc.want || completedCalls != before+1 || got.Completed != start {
				t.Fatalf("read proof outcome: %+v %v requests=%d", got, err, completedCalls-before)
			}
			if verified != (tc.proof != "" && tc.status == 204) {
				t.Fatal("verification ran without successful 204")
			}
			ops, _ := stage2Summarize([]stage2Observation{got})
			for _, op := range ops {
				if op.Operation == "read" && op.Endpoint == tc.endpoint && (op.Succeeded == 1) != (tc.want == "changed") {
					t.Fatal("unproven read counted as success")
				}
			}
			refuse(tc.endpoint, tc.id)
			page(tc.endpoint, tc.id)
			refuse(tc.endpoint, tc.id)
		})
	}
	status, d.verifyRead = 204, nil
	cursor = 100
	page("feed", 10)
	page("topic", 10)
	if _, err := drive("post", "feed", 10); err != nil {
		t.Fatal(err)
	}
	refuse("topic", 10)
	page("topic", 10)
	refuse("topic", 10) // Feed implicit read covers topics.
	cursor++
	page("feed", 10)
	page("topic", 10)
	if _, err := drive("read", "topic", 10); err != nil {
		t.Fatal(err)
	}
	refuse("feed", 10) // Topic write invalidates the overlapping feed snapshot.
	page("feed", 10)   // Topic coverage must not suppress other topics in the feed.
	if got, err := drive("read", "feed", 10); err != nil || got.Outcome != "unverified" {
		t.Fatalf("feed after topic: %+v %v", got, err)
	}
	page("topic", 10)
	refuse("topic", 10)
	cursor++
	page("topic", 12)
	if _, err := drive("post", "topic", 12); err != nil {
		t.Fatal(err)
	}
	page("topic", 12)
	refuse("topic", 12) // Topic posting's implicit read.
	unread = false
	cursor++
	page("feed", 11)
	refuse("feed", 11) // Own posts/unrelated events alone are insufficient.
	m, _ := d.readMember(stage2Slot{Endpoint: "feed", LogicalID: 11})
	m.admission.Lock()
	refuse("feed", 11)
	m.admission.Unlock()
}

func TestStage2ReadPageEvidence(t *testing.T) {
	valid := `<form id="read-form"><input type="hidden" name="cursor" value="42"><button type="submit">read</button></form>`
	for _, raw := range []string{"", valid + valid, strings.Replace(valid, "42", "9223372036854775808", 1), strings.TrimSuffix(valid, "</form>"), strings.Replace(valid, "42", "bogus", 1)} {
		if got := stage2ReadPageFrom([]byte(raw)); got.cursor != 0 || got.unread {
			t.Fatalf("invented page evidence: %+v", got)
		}
	}
	if got := stage2ReadPageFrom([]byte(valid)); got.cursor != 42 || !got.unread {
		t.Fatalf("lost snapshot evidence: %+v", got)
	}
}
