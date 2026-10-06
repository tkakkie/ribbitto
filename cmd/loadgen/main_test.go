package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafety(t *testing.T) {
	for _, target := range []string{"http://192.0.2.1", "http://example.com", "ftp://localhost", "http://user@localhost", "http://localhost/path", "http://localhost?x=1", "http://localhost/#x", "https://localhost"} {
		if _, err := newTransport(target, "", &counts{}); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
	for _, address := range []string{"127.2.3.4:80", "[::1]:80", "192.0.2.1:80", "[::2]:80", "localhost:80"} {
		want := strings.HasPrefix(address, "127.") || strings.HasPrefix(address, "[::1]")
		if (loopback("tcp", address, nil) == nil) != want {
			t.Errorf("dial guard: %s", address)
		}
	}
	for _, args := range [][]string{{"-duration=0"}, {"-duration=11m"}, {"-streams=0"}, {"-streams=100001"}, {"-rate=-1"}, {"-rate=101"}, {"-drain=0"}, {"-drain=6m"}, {"-setup=0"}, {"-setup=6m"}, {"-dial-concurrency=0"}, {"-dial-concurrency=100001"}} {
		if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "finite limits") {
			t.Errorf("bounds: %v: %v", args, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to := r.URL.Query().Get("to"); to != "" {
			http.Redirect(w, r, to, 302)
		}
	}))
	defer server.Close()
	tr, err := newTransport(server.URL, "", &counts{})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	if tr.Proxy != nil {
		t.Fatal("environment proxies enabled")
	}
	if conn, err := tr.DialContext(t.Context(), "tcp", "192.0.2.1:80"); err == nil {
		_ = conn.Close()
		t.Fatal("dial allowed non-loopback")
	}
	client := &http.Client{Transport: tr}
	// A redirect that stays on the origin is followed.
	if r, err := client.Get(server.URL + "?to=/done"); err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("same-origin redirect: %v", err)
	} else {
		_ = r.Body.Close()
	}
	for _, target := range []string{strings.Replace(server.URL, "http:", "https:", 1), strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "http://127.0.0.1:1"} {
		for _, endpoint := range []string{target, server.URL + "?to=" + target} {
			if r, err := client.Get(endpoint); err == nil {
				_ = r.Body.Close()
				t.Errorf("allowed origin change: %s", endpoint)
			}
		}
	}
}

func TestStreamsAndPosting(t *testing.T) {
	for _, status := range []int{200, 429, 503, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, err := r.Cookie("__Host-session")
				if err != nil || cookie.Value != "secret" {
					t.Error("missing session")
				}
				if r.Method == "POST" {
					if !strings.HasPrefix(r.FormValue("body"), "loadgen") || r.Header.Get("Origin") == "" || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
						t.Error("invalid post")
					}
					return
				}
				if r.Header.Get("Last-Event-ID") != "7" || !strings.HasSuffix(r.URL.Path, "/events") {
					t.Error("invalid stream request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, ": heartbeat\r\nid: 8\r\nevent: message\r\ndata: first\r\ndata: second\r\n\r\nevent: reset\r\ndata: {}\r\n\r\n")
			})))
			defer server.Close()
			path := t.TempDir() + "/tokens.json"
			if err := os.WriteFile(path, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret"]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := run([]string{"-target", server.URL, "-tokens", path, "-cursor=7", "-streams=2", "-duration=100ms", "-rate=50", "-drain=10ms"}, &out); err != nil {
				t.Fatal(err)
			}
			want := map[int]string{200: `"Established":2,"Refused429":0,"Refused503":0,"Reset":2,"Failed":0`, 429: `"Refused429":2`, 503: `"Refused503":2`, 400: `"Failed":2`}[status]
			if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), `"Answered200":0`) {
				t.Fatal(out.String())
			}
		})
	}
	for _, body := range []string{"event: reset\n\n", "event: reset\ndata: {}", "event: reset\nevent: message\ndata: {}\n\n"} {
		if readReset(strings.NewReader(body), nil) {
			t.Fatal("dispatched incomplete/non-reset event")
		}
	}
}

func TestHTTP2AndTrust(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("HTTP/2 not negotiated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c := &counts{}
	tr, err := newTransport(server.URL, path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// Establish serially so the second stream must reuse the multiplexed connection.
	a, err := request(ctx, client, "GET", server.URL, "secret", "0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Body.Close() }()
	b, err := request(ctx, client, "GET", server.URL, "secret", "0")
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Body.Close()
	if c.tcp.Load() != 1 {
		t.Fatal("streams counted as TCP connections")
	}
	// A fresh transport with an empty pool trusts neither system nor test roots.
	untrusted, err := newTransport(strings.Replace(server.URL, "https:", "http:", 1), "", &counts{})
	if err != nil {
		t.Fatal(err)
	}
	untrusted.origin.Scheme = "https"
	defer untrusted.CloseIdleConnections()
	if r, err := (&http.Client{Transport: untrusted}).Get(server.URL); err == nil {
		_ = r.Body.Close()
		t.Fatal("trusted an unconfigured CA")
	}
}

// The end of a run is the harness's doing: a POST in flight at the deadline
// finishes and counts; established streams close without failures. Streams
// still connecting at the setup deadline are failures.
func TestRunEndIsNotAFailure(t *testing.T) {
	deadline := 100 * time.Millisecond
	for _, tc := range []struct {
		name, args, want string
	}{
		{"post across the deadline", "-rate=20", `"Answered200":2,"PostFailed":0`},
		{"stream before its headers", "-rate=0", `"Established":0,"Refused429":0,"Refused503":0,"Reset":0,"Failed":1`},
		{"established stream", "-rate=0", `"Established":1,"Refused429":0,"Refused503":0,"Reset":0,"Failed":0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A POST is held from its arrival for longer than the whole run.
			// It arrives after the run started, so it is still in flight
			// when the run's deadline passes, whatever the setup took.
			var finished atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					time.Sleep(deadline + 50*time.Millisecond)
					finished.Store(time.Now().UnixNano())
					return
				}
				if tc.name != "stream before its headers" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			path := t.TempDir() + "/tokens.json"
			if err := os.WriteFile(path, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret"]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			start := time.Now()
			if err := run([]string{"-target", server.URL, "-tokens", path, "-duration=" + deadline.String(), "-cursor=0", "-setup=100ms", "-drain=10ms", tc.args}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("%s: want %q", out.String(), tc.want)
			}
			if tc.name == "post across the deadline" && time.Duration(finished.Load()-start.UnixNano()) <= deadline {
				t.Fatal("the POST finished before the run's deadline")
			}
		})
	}
}

func TestStep(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		hold, delay, duration    time.Duration
		rate                     int
		want                     string
		missing, reset, explicit bool
	}{
		{name: "event before response", hold: 70 * time.Millisecond, duration: 40 * time.Millisecond, rate: 50, want: "pass"},
		{name: "response before event", delay: 20 * time.Millisecond, duration: 40 * time.Millisecond, rate: 50, want: "pass", explicit: true},
		{name: "missing", duration: 40 * time.Millisecond, rate: 50, want: "fail", missing: true},
		{name: "reset idle", duration: 40 * time.Millisecond, want: "fail", reset: true},
		{name: "underloaded", hold: 1200 * time.Millisecond, duration: 1100 * time.Millisecond, rate: 1, want: "underloaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan string, 100)
			var pageReads atomic.Int64
			server := httptest.NewServer(http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					marker := r.FormValue("body")
					if tc.delay > 0 {
						time.AfterFunc(tc.delay, func() { events <- marker })
						return
					}
					if !tc.missing {
						events <- marker
					}
					time.Sleep(tc.hold)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/events") {
					pageReads.Add(1)
					_, _ = fmt.Fprint(w, `<div sse-connect="/events?after=7"></div>`)
					return
				}
				want := "7"
				if tc.explicit {
					want = "9"
				}
				if r.Header.Get("Last-Event-ID") != want {
					t.Error("cursor not shared from page/override")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_ = http.NewResponseController(w).Flush()
				if tc.reset {
					_, _ = fmt.Fprint(w, "event: reset\ndata: {}\n\n")
					return
				}
				for {
					select {
					case marker := <-events:
						// A rendered message can repeat its body in an attribute and its text.
						_, _ = fmt.Fprintf(w, "event: message\ndata: <li title=\"%s\">%s</li>\n\n", marker, marker)
						if tc.hold > 0 {
							_, _ = fmt.Fprintf(w, "data: %s\n\n", marker)
						}
						_ = http.NewResponseController(w).Flush()
					case <-r.Context().Done():
						return
					}
				}
			})))
			defer server.Close()
			path := t.TempDir() + "/tokens.json"
			if err := os.WriteFile(path, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret"]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-target", server.URL, "-tokens", path, "-duration=" + tc.duration.String(), fmt.Sprintf("-rate=%d", tc.rate), "-drain=50ms", "-dial-concurrency=1"}
			if tc.explicit {
				args = append(args, "-cursor=9")
			}
			var out bytes.Buffer
			if err := run(args, &out); err != nil {
				t.Fatal(err)
			}
			var got result
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Verdict != tc.want || got.Established != 1 || got.Expected != got.Answered200 || got.Scheduled != got.Sent+got.Missed || got.Answered200 != got.Sent || got.RequestFailed != tc.reset || strings.Contains(out.String(), "secret") {
				t.Fatalf("bad step: %s", out.String())
			}
			if (pageReads.Load() == 0) != tc.explicit || !got.CursorValid || got.CursorOverride != tc.explicit || (got.Missing > 0) != tc.missing || (!tc.missing && got.Received != got.Expected) || ((tc.hold > 0 && got.Duplicated != got.Received) || (tc.hold == 0 && got.Duplicated != 0)) {
				t.Fatalf("bad accounting: %s", out.String())
			}
			if tc.delay > 0 {
				got.verdict(time.Nanosecond)
				if got.Verdict != "fail" || !got.Slow {
					t.Fatal("latency threshold ignored")
				}
			}
		})
	}
}

func TestVerdict(t *testing.T) {
	for _, r := range []result{{P95MS: 2}, {Missing: 1}, {Refused429: 1}, {Refused503: 1}, {Reset: 1}, {Failed: 1}, {PostFailed: 1}, {Missed: 1}, {Scheduled: 1}} {
		r.verdict(time.Millisecond)
		want := "fail"
		if r.Missed > 0 || r.Scheduled > 0 {
			want = "underloaded"
		}
		if r.Verdict != want {
			t.Fatalf("%+v", r)
		}
	}
}
