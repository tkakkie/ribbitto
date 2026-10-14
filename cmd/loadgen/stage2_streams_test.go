package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage2Streams(t *testing.T) {
	for _, member := range []uint64{0, 1, 3} {
		t.Run(fmt.Sprint(member), func(t *testing.T) {
			var streams, pages atomic.Int64
			ready := make(chan struct{}, 1)
			frames := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, _ := r.Cookie("__Host-session")
				if cookie == nil || cookie.Value != fmt.Sprintf("session-%d", member) || r.Proto != "HTTP/1.1" {
					t.Error("stream authentication or HTTP/1.1")
				}
				if !strings.HasSuffix(r.URL.Path, "/events") {
					pages.Add(1)
					path := "/organizations/paper-lantern/channels/00000000-0000-0000-0000-000000000001"
					if member%2 == 1 {
						path += fmt.Sprintf("/topics/00000000-0000-0000-0000-%012d", 21+(member/2)%2)
					}
					if r.URL.Path != path {
						t.Error("wrong establishment page")
					}
					_, _ = fmt.Fprint(w, `<form id="read-form"><input type="hidden" name="cursor" value="40"><button type="submit">read</button></form>`)
					return
				}
				streams.Add(1)
				q := r.URL.Query()
				topic := ""
				if member%2 == 1 {
					topic = fmt.Sprintf("00000000-0000-0000-0000-%012d", 21+(member/2)%2)
				}
				if r.URL.Path != "/organizations/paper-lantern/events" || q.Get("want") != "sidebar,messages,typing" || q.Get("channel") != "00000000-0000-0000-0000-000000000001" || q.Get("topic") != topic || r.Header.Get("Last-Event-ID") != "40" || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("stream scope or retained cursor")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-frames:
				}
				{
					id := uint64(1)
					if member == 1 {
						id = 0
					}
					if member == 3 {
						id = 2
					}
					_, _ = fmt.Fprintf(w, "id: 100\nevent: message\ndata: declined\n\nid: 42\nevent: message\ndata: stage2-post:topic:%d\n\nid: 43\nevent: messages-moved\ndata: moved\n\nid: 99\nevent: typing\ndata: ephemeral\n\nid: 100\nevent: unknown\ndata: unknown\n\nid: 9223372036854775808\nevent: message\ndata: invalid\n\nevent: message\ndata: no-id\n\nevent: typing\ndata: ready\n\nid: 90\nevent: message\ndata: partial", id)
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			d, err := newStage2Driver(server.URL, stage2CredentialPath(t, stage2CredentialJSON()), fixedStage2().Groups[0], 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.transport.CloseIdleConnections()
			start := time.Unix(123, 0)
			now := start
			clock := func() time.Time { return now }
			apply := func(_ uint64, _ string, data string) bool {
				if data == "ready" {
					ready <- struct{}{}
				}
				return data != "declined"
			}
			s, attempts, err := d.establishStream(t.Context(), member, start, clock, apply)
			if err != nil || len(attempts) != 2 {
				t.Fatalf("establishment: %v %+v", err, attempts)
			}
			defer func() {
				if err := s.close(); err != nil {
					t.Error(err)
				}
			}()
			if duplicate, _, err := d.establishStream(t.Context(), member, start, clock, apply); err == nil {
				_ = duplicate.close()
				t.Fatal("duplicate member stream")
			}
			m := d.members[member]
			m.mu.Lock()
			if c, ok := m.eligible(m.streamScope); !ok || c != 40 || m.applied != 40 {
				t.Errorf("initial page evidence: %d %v applied=%d", c, ok, m.applied)
			}
			m.reserve(m.streamScope, 40)
			m.mu.Unlock()
			close(frames)
			<-ready
			m.mu.Lock()
			if c, ok := m.eligible(m.streamScope); !ok || c != 43 {
				t.Errorf("applied durable evidence: %d %v", c, ok)
			}
			m.applyPage(m.streamScope, stage2ReadPage{200, true}) // Page evidence must not skip replay.
			m.mu.Unlock()
			if err := s.close(); err != nil {
				t.Fatal(err)
			}
			if s.cursor != 43 {
				t.Fatalf("non-applied frame advanced reconnect cursor: %d", s.cursor)
			}
			if streams.Load() != 1 || pages.Load() != 1 {
				t.Fatal("establishment issued extra requests")
			}
			// Idempotent close must release ownership exactly once.
			if err := s.close(); err != nil || !d.members[member].stream.TryLock() {
				t.Fatal("close did not release member stream")
			}
			d.members[member].stream.Unlock()
		})
	}
}

func TestStage2StreamFailures(t *testing.T) {
	for _, failure := range []string{"page503", "page-empty", "initial503", "429", "500", "bad-type", "http10", "reset", "eof", "cancel", "headers-cancel"} {
		t.Run(failure, func(t *testing.T) {
			var connects atomic.Int64
			headers := make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/events") {
					if failure == "page503" {
						w.WriteHeader(503)
					} else if failure != "page-empty" {
						_, _ = fmt.Fprint(w, `<form id="read-form"><input type="hidden" name="cursor" value="40"></form>`)
					}
					return
				}
				connects.Add(1)
				if failure == "headers-cancel" {
					close(headers)
					<-r.Context().Done()
					return
				}
				if failure == "initial503" || failure == "429" || failure == "500" {
					status := map[string]int{"initial503": 503, "429": 429, "500": 500}[failure]
					w.WriteHeader(status)
					return
				}
				if failure == "bad-type" {
					return
				}
				if failure == "http10" {
					// A correct status and type over HTTP/1.0 is still not an HTTP/1.1 stream.
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_, _ = buf.WriteString("HTTP/1.0 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
						_ = buf.Flush()
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if failure == "reset" {
					_, _ = fmt.Fprint(w, "id: 90\nevent: reset\ndata: reset\n\n")
					return
				}
				w.(http.Flusher).Flush()
				if failure != "eof" {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			d, err := newStage2Driver(server.URL, stage2CredentialPath(t, stage2CredentialJSON()), fixedStage2().Groups[0], 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.transport.CloseIdleConnections()
			if failure == "headers-cancel" {
				go func() { <-headers; cancel() }()
			}
			start := time.Unix(123, 0)
			s, attempts, err := d.establishStream(ctx, 0, start, func() time.Time { return start }, func(uint64, string, string) bool { return true })
			for _, a := range attempts {
				if a.Operation != "establishment" || a.Endpoint != "feed" || a.LogicalID != 0 || a.Attempt != 1 {
					t.Fatalf("establishment attempt identity: %+v", a)
				}
			}
			if failure != "reset" && failure != "eof" && failure != "cancel" {
				want := 2
				if strings.HasPrefix(failure, "page") {
					want = 1
				}
				if err == nil || s != nil || len(attempts) != want || connects.Load() != int64(want-1) {
					if s != nil {
						_ = s.close()
					}
					t.Fatalf("establishment failure lost or retried: %v %+v", err, attempts)
				}
				status := map[string]int{"page503": 503, "page-empty": 200, "initial503": 503, "429": 429, "500": 500, "bad-type": 200, "http10": 200}[failure]
				if a := attempts[want-1]; a.Status != status || a.Outcome != "failed" && failure != "page-empty" {
					t.Fatalf("establishment status or outcome: %+v", a)
				}
				if !d.members[0].stream.TryLock() {
					t.Fatal("failed establishment retained member stream")
				}
				d.members[0].stream.Unlock()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.close() }()
			if failure == "cancel" {
				cancel()
			}
			<-s.done
			err = s.close()
			if (err == nil) != (failure == "cancel") || s.cursor != 40 {
				t.Fatalf("stream lifecycle error or cursor: %v cursor=%d", err, s.cursor)
			}
		})
	}
}
