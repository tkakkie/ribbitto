package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stage2CredentialJSON() string {
	var accounts strings.Builder
	for i := range 1000 {
		if i > 0 {
			accounts.WriteByte(',')
		}
		fmt.Fprintf(&accounts, `{"member_id":"00000000-0000-0000-0000-%012d","handle":"stage2-%d","tokens":["session-%d"]}`, i+100, i+1, i)
	}
	return fmt.Sprintf(`{"organization_slug":"paper-lantern","channel_ids":["00000000-0000-0000-0000-000000000001","00000000-0000-0000-0000-000000000002","00000000-0000-0000-0000-000000000003","00000000-0000-0000-0000-000000000004"],"accounts":[%s],"stage2":{"version":1,"fixture_seed":0,"hot_channel_id":"00000000-0000-0000-0000-000000000001","default_topic_ids":["00000000-0000-0000-0000-000000000011","00000000-0000-0000-0000-000000000012","00000000-0000-0000-0000-000000000013","00000000-0000-0000-0000-000000000014"],"named_topics":[{"id":"00000000-0000-0000-0000-000000000021","name":"stage2-topic-1"},{"id":"00000000-0000-0000-0000-000000000022","name":"stage2-topic-2"}]}}`, &accounts)
}

func stage2CredentialPath(t *testing.T, raw string) string {
	t.Helper()
	path := t.TempDir() + "/credentials.json"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStage2Credentials(t *testing.T) {
	raw, group := stage2CredentialJSON(), fixedStage2().Groups[0]
	for _, tc := range []struct{ name, old, replacement string }{
		{"valid zero seed", "", ""},
		{"version", `"version":1`, `"version":2`},
		{"seed", `"fixture_seed":0`, `"fixture_seed":1`},
		{"missing seed", `"fixture_seed":0,`, ``},
		{"null seed", `"fixture_seed":0`, `"fixture_seed":null`},
		{"duplicate version", `"version":1`, `"version":2,"version":1`},
		{"channel count", `,"00000000-0000-0000-0000-000000000004"`, ``},
		{"default count", `,"00000000-0000-0000-0000-000000000014"`, ``},
		{"topic count", `,{"id":"00000000-0000-0000-0000-000000000022","name":"stage2-topic-2"}`, ``},
		{"hot channel", `"hot_channel_id":"00000000-0000-0000-0000-000000000001"`, `"hot_channel_id":"00000000-0000-0000-0000-000000000002"`},
		{"member count", `,{"member_id":"00000000-0000-0000-0000-000000001099","handle":"stage2-1000","tokens":["session-999"]}`, ``},
		{"duplicate member", `000000000101`, `000000000100`},
		{"invalid ID", `000000000100`, `invalid-id`},
		{"duplicate session", `"session-1"`, `"session-0"`},
		{"missing session", `["session-0"]`, `[]`},
		{"extra session", `["session-0"]`, `["session-0","other"]`},
		{"invalid cookie", `"session-0"`, `"bad;cookie"`},
		{"slug", `"paper-lantern"`, `".."`},
		{"topic name", `"stage2-topic-1"`, `"other"`},
		{"trailing JSON", `}}`, `}} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := raw
			if tc.old != "" {
				changed = strings.Replace(raw, tc.old, tc.replacement, 1)
				if changed == raw {
					t.Fatal("ineffective test mutation")
				}
			}
			_, err := readStage2Credentials(stage2CredentialPath(t, changed), group, 0)
			if (err == nil) != (tc.name == "valid zero seed") {
				t.Fatalf("credential validation: %s: %v", tc.name, err)
			}
			if err != nil && strings.Contains(err.Error(), "session-") {
				t.Fatal("credential error leaked token")
			}
		})
	}
	path := stage2CredentialPath(t, raw)
	for _, g := range []stage2Group{fixedStage2().Groups[2], {Streams: 1000}} {
		if _, err := readStage2Credentials(path, g, 0); err == nil {
			t.Fatal("accepted mismatched or unknown group")
		}
	}
	if _, err := readStage2Credentials(path+"-absent", group, 0); err == nil {
		t.Fatal("accepted absent file")
	}
}

func TestStage2Drivers(t *testing.T) {
	path := stage2CredentialPath(t, stage2CredentialJSON())
	for _, operation := range []string{"post", "page"} {
		for _, endpoint := range []string{"feed", "topic"} {
			for _, tc := range []struct {
				id            uint64
				member, topic int
			}{{0, 0, 21}, {1, 1, 21}, {2, 2, 22}, {3, 3, 22}, {1000, 0, 21}, {1003, 3, 22}} {
				t.Run(fmt.Sprintf("%s/%s/%d", operation, endpoint, tc.id), func(t *testing.T) {
					var received atomic.Int64
					server := httptest.NewServer(http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received.Add(1)
						wantPath := "/organizations/paper-lantern/channels/00000000-0000-0000-0000-000000000001"
						if endpoint == "topic" {
							wantPath += fmt.Sprintf("/topics/00000000-0000-0000-0000-%012d", tc.topic)
						}
						if r.URL.Path != wantPath || r.URL.RawQuery != "" {
							t.Errorf("endpoint split: %s, want %s", r.URL, wantPath)
						}
						cookie, err := r.Cookie("__Host-session")
						if err != nil || cookie.Value != fmt.Sprintf("session-%d", tc.member) || len(r.Cookies()) != 1 {
							t.Error("member rotation: incorrect session")
						}
						if r.Proto != "HTTP/1.1" {
							t.Errorf("protocol: %s", r.Proto)
						}
						if operation == "post" {
							if r.Method != "POST" || r.FormValue("body") != stage2PostMarker(stage2Slot{Endpoint: endpoint, LogicalID: tc.id})+" marker & 日本語" || r.FormValue("cursor") != "90" || len(r.PostForm) != 2 || r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
								t.Error("invalid authenticated post form/CSRF headers")
							}
							hx := ""
							if endpoint == "feed" {
								hx = "true"
							}
							if r.Header.Get("HX-Request") != hx {
								t.Error("incorrect post enhancement")
							}
							if endpoint == "topic" {
								w.Header().Set("Location", "/unexpected-redirect")
								w.WriteHeader(303)
							}
						} else {
							body, _ := io.ReadAll(r.Body)
							if r.Method != "GET" || len(body) != 0 || r.Header.Get("HX-Request") != "" || r.Header.Get("Last-Event-ID") != "" {
								t.Error("invalid full page request")
							}
						}
					})))
					defer server.Close()
					d, err := newStage2Driver(server.URL, path, fixedStage2().Groups[0], 0)
					if err != nil {
						t.Fatal(err)
					}
					defer d.transport.CloseIdleConnections()
					m, scope := d.readMember(stage2Slot{Endpoint: endpoint, LogicalID: tc.id})
					m.applyPage(scope, stage2ReadPage{cursor: 77})
					d.applyFrame(uint64(tc.member), 90, "")
					start := time.Unix(123, 0)
					o := stage2Observation{Slot: stage2Slot{Operation: operation, Endpoint: endpoint, LogicalID: tc.id, At: start}}
					got, err := d.drive(t.Context(), o, start, func() time.Time { return start }, "marker & 日本語")
					if err != nil || got.Outcome != "succeeded" || len(got.Attempts) != 1 || received.Load() != 1 || !got.MemberSelected || got.Member != uint64(tc.member) {
						t.Fatalf("one exchange with operation success status: %+v, %v, requests=%d", got, err, received.Load())
					}
					if operation == "post" && (m.used[scope] != 90 || len(m.pages) != 0) {
						t.Fatal("post did not reserve newest applied cursor and invalidate pages")
					}
				})
			}
		}
	}
}

func TestStage2DriverBoundaries(t *testing.T) {
	path, group := stage2CredentialPath(t, stage2CredentialJSON()), fixedStage2().Groups[0]
	for _, target := range []string{"https://localhost", "http://192.0.2.1", "http://localhost/path", "http://user@localhost", "http://localhost?x=1"} {
		if _, err := newStage2Driver(target, path, group, 0); err == nil {
			t.Fatalf("accepted target %s", target)
		}
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	d, err := newStage2Driver(server.URL, path, group, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.transport.CloseIdleConnections()
	if d.transport.Proxy != nil || d.transport.ForceAttemptHTTP2 {
		t.Fatal("stage-2 enabled proxies or HTTP/2")
	}
	for _, slot := range []stage2Slot{{Operation: "other", Endpoint: "feed"}, {Operation: "replacement", Endpoint: "topic"}, {Operation: "page", Endpoint: "other"}} {
		if _, err := d.request(t.Context(), slot, "", 0, 0); err == nil {
			t.Fatal("accepted unsupported slot")
		}
	}
	for _, endpoint := range []string{"feed", "topic"} {
		o := stage2Observation{Slot: stage2Slot{Operation: "post", Endpoint: endpoint}}
		req, err := d.request(t.Context(), o.Slot, "marker", 0, 0)
		if err != nil || req.GetBody != nil {
			t.Fatal("POST body may be replayed")
		}
		got, err := d.drive(t.Context(), o, time.Time{}, time.Now, "marker")
		if err != nil || got.Outcome != "failed" || len(got.Attempts) != 1 {
			t.Fatalf("503 must fail once: %+v, %v", got, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("POST retried: requests=%d", requests.Load())
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1:1", nil)
	if response, err := d.transport.RoundTrip(req); err == nil {
		_ = response.Body.Close()
		t.Fatal("allowed origin change")
	}
}
