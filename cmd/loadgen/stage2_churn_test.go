package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage2Churn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		member  uint64
		outcome string
		status  int
	}{
		{"feed", 0, "succeeded", 200}, {"topic1", 1, "succeeded", 200}, {"topic2", 3, "succeeded", 200},
		{"429", 0, "failed", 429}, {"500", 0, "failed", 500}, {"bad-type", 0, "failed", 200},
		{"503-bad-type", 0, "failed", 200},
		{"refusals", 0, "timeout", 503}, {"wait-cancel", 0, "timeout", 0}, {"headers-cancel", 0, "failed", 0},
		{"busy", 0, "failed", 0}, {"wrong-scope", 0, "failed", 0}, {"closed", 0, "failed", 0},
		{"reset", 0, "failed", 0}, {"eof", 0, "failed", 0}, {"caller-cancel", 0, "failed", 0},
		{"expired", 0, "missed", 0}, {"missed", 0, "missed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streams, pages atomic.Int64
			ready, stopped, headers := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/events") {
					pages.Add(1)
					_, _ = fmt.Fprint(w, `<form id="read-form"><input type="hidden" name="cursor" value="40"></form>`)
					return
				}
				attempt := streams.Add(1)
				cookie, _ := r.Cookie("__Host-session")
				topic := ""
				if tc.member%2 == 1 {
					topic = fmt.Sprintf("00000000-0000-0000-0000-%012d", 21+(tc.member/2)%2)
				}
				cursor := "43"
				if attempt == 1 {
					cursor = "40"
				}
				q := r.URL.Query()
				if cookie == nil || cookie.Value != fmt.Sprintf("session-%d", tc.member) || r.Proto != "HTTP/1.1" || r.Header.Get("Last-Event-ID") != cursor || q.Get("topic") != topic || q.Get("want") != "sidebar,messages,typing" || q.Get("channel") != "00000000-0000-0000-0000-000000000001" {
					t.Error("reconnect authentication, scope or last complete cursor")
				}
				if attempt > 1 {
					if tc.name == "headers-cancel" {
						close(headers)
						<-r.Context().Done()
						return
					}
					if tc.outcome == "succeeded" && attempt <= 3 || tc.name == "refusals" || tc.name == "503-bad-type" && attempt == 2 {
						w.WriteHeader(503)
						return
					}
					if tc.outcome == "failed" {
						status := tc.status
						if status == 0 {
							status = 500
						}
						w.WriteHeader(status)
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if attempt == 1 {
					defer close(stopped)
					if tc.name == "reset" {
						_, _ = fmt.Fprint(w, "event: reset\ndata: reset\n\n")
						return
					}
					if tc.name == "eof" {
						return
					}
					_, _ = fmt.Fprint(w, "id: 43\nevent: messages-moved\ndata: complete\n\nid: 100\nevent: message\ndata: declined\n\nid: 99\nevent: typing\ndata: ready\n\nid: 90\nevent: message\ndata: partial")
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
			now := start.Add(time.Second) // Outage excludes time before the scheduled close.
			clock := func() time.Time { return now }
			apply := func(_ uint64, _ string, data string) bool {
				if data == "ready" {
					ready <- struct{}{}
				}
				return data != "declined"
			}
			s, initial, err := d.establishStream(t.Context(), tc.member, start, clock, apply)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.close() }()
			if tc.name == "reset" || tc.name == "eof" {
				<-s.done
			} else {
				<-ready
			}
			m := d.members[tc.member]
			m.mu.Lock()
			m.applyPage(m.streamScope, stage2ReadPage{200, true}) // A later snapshot must not skip replay.
			m.mu.Unlock()
			slot := stage2Slot{"replacement", stage2StreamPage(tc.member).Endpoint, 7, now, now.Add(time.Second)}
			o := stage2Observation{Slot: slot}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tc.name {
			case "busy":
				s.mu.Lock()
			case "wrong-scope":
				o.Slot.Endpoint = "topic"
			case "closed":
				_ = s.close()
			case "caller-cancel":
				cancel()
			case "expired":
				now = slot.Expires
			case "missed":
				o.Outcome, o.Completed = "missed", now
			}
			if tc.name == "headers-cancel" {
				go func() { <-headers; cancel() }()
			}
			offline := now
			var delays []time.Duration
			o, err = s.replace(ctx, o, start, 865, clock, func(_ context.Context, delay time.Duration) bool {
				<-stopped // The old stream must close before the reconnect delay starts.
				if m.stream.TryLock() {
					m.stream.Unlock()
					t.Error("replacement released member ownership while offline")
				}
				if delay < 250*time.Millisecond || delay > 500*time.Millisecond {
					t.Error("churn delay outside protocol")
				}
				delays = append(delays, delay)
				now = now.Add(delay)
				if tc.name == "wait-cancel" {
					cancel()
				}
				return ctx.Err() == nil
			})
			if tc.name == "busy" {
				s.mu.Unlock()
			}
			wantErr := tc.status == 0 && tc.outcome == "failed" && tc.name != "headers-cancel"
			if (err != nil) != wantErr || o.Outcome != tc.outcome {
				t.Fatalf("replacement outcome: %s error=%v", o.Outcome, err)
			}
			if tc.name != "wrong-scope" && !reflect.DeepEqual(o.Slot, slot) {
				t.Fatal("replacement changed scheduled slot")
			}
			if pages.Load() != 1 || streams.Load() != int64(1+len(o.Attempts)) {
				t.Fatal("replacement issued extra requests")
			}
			for i, a := range o.Attempts {
				if a.Operation != "replacement" || a.Endpoint != slot.Endpoint || a.LogicalID != 7 || a.Attempt != uint64(i+1) || a.OutageNS != a.OffsetNS-offline.Sub(start).Nanoseconds() {
					t.Fatalf("attempt identity or offline outage: %+v", a)
				}
			}
			if tc.outcome == "succeeded" {
				if len(o.Attempts) != 3 || o.Attempts[0].Status != 503 || o.Attempts[1].Status != 503 || o.Attempts[2].Status != 200 || !o.MemberSelected || o.Member != tc.member {
					t.Fatalf("replacement attempts or chosen member: %+v", o)
				}
				if !reflect.DeepEqual(delays, []time.Duration{458312291, 369481906, 420547883}) {
					t.Fatalf("seeded churn delays changed: %v", delays)
				}
				ops, all := stage2Summarize([]stage2Observation{o, {Slot: stage2Slot{Operation: "replacement", Endpoint: "topic", LogicalID: 8}, Outcome: "missed", Completed: now}})
				r, normal := stage2CountRefusals(ops, append(initial, all...))
				want := stage2Refusals{ScheduledReplacements: 2, AffectedReplacements: 1, Recovered: 1, Attempts: 3, Refused503: 2, Retries: 2, MaxOutageNS: now.Sub(offline).Nanoseconds()}
				if r != want || normal != 0 {
					t.Fatalf("logical refusal accounting: %+v normal=%d", r, normal)
				}
				later := all[2]
				later.Attempt, later.OutageNS = 4, 0
				first, _ := stage2CountRefusals(ops, []stage2Attempt{later, all[2], all[1], all[0]})
				if first.Recovered != 1 || first.MaxOutageNS != want.MaxOutageNS {
					t.Fatalf("first reconnect replaced by later success: %+v", first)
				}
			} else if tc.name == "refusals" {
				if len(o.Attempts) != 5 || now.Sub(offline) != 1954305985*time.Nanosecond {
					t.Fatal("refusal retry budget absent or exceeded")
				}
				ops, all := stage2Summarize([]stage2Observation{o})
				r, _ := stage2CountRefusals(ops, all)
				if r.ScheduledReplacements != 1 || r.AffectedReplacements != 1 || r.Recovered != 0 || r.Refused503 != uint64(len(all)) || ops[6].Timeouts != 1 {
					t.Fatalf("unrecovered refusal lost: %+v %+v", r, ops[6])
				}
			} else if tc.status != 0 || tc.name == "headers-cancel" {
				count := 1
				if tc.name == "503-bad-type" {
					count = 2
					ops, all := stage2Summarize([]stage2Observation{o})
					r, _ := stage2CountRefusals(ops, all)
					if r.AffectedReplacements != 1 || r.Recovered != 0 || ops[6].Failed != 1 {
						t.Fatalf("invalid 200 counted as recovery: %+v %+v", r, ops[6])
					}
				}
				if len(o.Attempts) != count || len(delays) != count || o.Attempts[count-1].Status != tc.status || o.Attempts[count-1].Outcome != "failed" {
					t.Fatal("other reconnect failure lost or retried")
				}
			} else if len(o.Attempts) != 0 || len(delays) != 0 && tc.name != "wait-cancel" {
				t.Fatal("unavailable replacement stopped or reconnected stream")
			}
			switch tc.name {
			case "busy", "wrong-scope", "caller-cancel", "expired", "missed":
				select {
				case <-stopped:
					t.Fatal("unavailable replacement closed the existing stream")
				default:
				}
			}
			if err := s.close(); err != nil && tc.name != "reset" && tc.name != "eof" {
				t.Fatal(err)
			}
			if !m.stream.TryLock() {
				t.Fatal("replacement cleanup retained stream ownership")
			}
			m.stream.Unlock()
		})
	}
}
