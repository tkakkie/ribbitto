package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	for _, args := range [][]string{{"-duration=0"}, {"-duration=11m"}, {"-streams=0"}, {"-streams=100001"}, {"-rate=-1"}, {"-rate=101"}, {"-drain=0"}, {"-drain=6m"}, {"-setup=0"}, {"-setup=6m"}, {"-dial-concurrency=0"}, {"-dial-concurrency=100001"}, {"-reconnect-delay=-1ns"}, {"-reconnect-delay=10.001s"}, {"-reconnect-jitter=-1ns"}, {"-reconnect-jitter=10.001s"}, {"-post-attempts=0"}, {"-post-attempts=11"}} {
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
			var got result
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tc.name == "stream before its headers" && got.SetupSeconds >= 1 {
				t.Fatalf("setup exceeded 1s with -setup=100ms: %s", out.String())
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
		{name: "event after drain", delay: 200 * time.Millisecond, duration: 40 * time.Millisecond, rate: 1, want: "fail", missing: true},
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
			drain := "50ms"
			if tc.want == "pass" {
				drain = "1s"
			}
			args := []string{"-target", server.URL, "-tokens", path, "-duration=" + tc.duration.String(), fmt.Sprintf("-rate=%d", tc.rate), "-drain=" + drain, "-dial-concurrency=1"}
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
			if tc.want == "pass" && got.DrainSeconds >= got.DrainLimitSeconds {
				t.Fatalf("all receipts should end drain early: %s", out.String())
			}
			if tc.name == "event after drain" && got.Missing != 1 {
				t.Fatalf("late receipt should be missing: %s", out.String())
			}
			if tc.delay > 0 && !tc.missing {
				got.verdict(time.Nanosecond)
				if got.Verdict != "fail" || !got.Slow {
					t.Fatal("latency threshold ignored")
				}
			}
		})
	}
}

func TestVerdict(t *testing.T) {
	for _, r := range []result{{P95MS: 2}, {Missing: 1}, {Refused429: 1}, {Refused503: 1}, {Reset: 1}, {Failed: 1}, {PostFailed: 1}, {Missed: 1}, {Missed: 1, Reset: 1}, {Scheduled: 1}} {
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

// Cancellation can close HTTP streams before a late event reaches receive.
// Exercise both cutoffs directly so transport cancellation cannot mask them.
func TestReceiveAfterDrain(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		p := &post{sent: time.Now(), answered: true}
		c := &counts{markers: regexp.MustCompile("late"), deliveries: map[string]*post{"late": p}, frozen: frozen}
		if !frozen {
			c.deadline = time.Now().Add(-time.Millisecond)
		}
		c.receive(1, "late", make(map[*post]bool))
		if len(p.latencies) != 0 || c.received.Load() != 0 {
			t.Fatalf("late receipt counted (frozen=%t)", frozen)
		}
	}
}

func TestDiagnostics(t *testing.T) {
	var open, requests, snapshots atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		if peer != "127.0.0.1" || r.Header.Get("Accept-Language") != "" {
			t.Errorf("unexpected peer/language: %s", r.RemoteAddr)
		}
		requests.Add(1)
		if r.Method == "POST" {
			body := r.FormValue("body")
			if len(body) != 4000 || !strings.HasPrefix(body, "loadgen") || !strings.HasSuffix(body, "&") {
				t.Error("invalid padding")
			}
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/events") {
			_, _ = fmt.Fprint(w, `<div sse-connect="/events?after=7"></div>`)
			return
		}
		open.Add(1)
		defer open.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 10\nevent: presence\ndata: ignored\n\nid: 9\nevent: message\ndata: short\n\nid: 8\nevent: message\ndata: first\ndata: second\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || len(r.Cookies()) != 0 {
			t.Error("invalid metrics request")
		}
		w.Header().Set("Connection", "close")
		n := snapshots.Add(1)
		_, _ = fmt.Fprintf(w, `{"streams":{"open":%d},"runtime":{"goroutines":42,"heap_inuse_bytes":4096},"pool":{"total_conns":3,"max_conns":8,"empty_acquire_count":%d,"empty_acquire_wait_ns":%d},"database":{"queries":%d,"transactions_begun":%d}}`, open.Load(), n*3, n*4, n*10, n*2)
	}))
	defer metrics.Close()
	path := t.TempDir() + "/tokens.json"
	if err := os.WriteFile(path, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Invalid origins and source lists must be rejected before even the page request.
	for _, args := range [][]string{{"-metrics=http://192.0.2.1"}, {"-metrics=https://localhost"}, {"-source=192.0.2.1"}, {"-source=::1"}, {"-source=::ffff:127.0.0.1"}, {"-source=localhost"}, {"-source=127.0.0.1,"}, {"-source=" + strings.Repeat("127.0.0.1,", 64) + "127.0.0.1"}, {"-body-length=-1"}, {"-body-length=4001"}} {
		if err := run(append([]string{"-target", server.URL, "-tokens", path}, args...), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if requests.Load() != 0 || snapshots.Load() != 0 {
		t.Fatal("requested before validation")
	}
	var out bytes.Buffer
	if err := run([]string{"-target", server.URL, "-metrics", metrics.URL + "/", "-source=" + strings.TrimSuffix(strings.Repeat("127.0.0.1,", 64), ","), "-tokens", path, "-streams=2", "-duration=30ms", "-rate=1", "-drain=1ms", "-body-length=4000", "-body-escape"}, &out); err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	m := got.Server
	if snapshots.Load() != 3 || m == nil {
		t.Fatalf("missing snapshots: %s", out.String())
	}
	if m.Before.Streams.Open != 0 || m.After.Streams.Open != 2 || m.Closed.Streams.Open != 0 || m.Before.Database.Queries != 10 || m.After.Database.Queries != 20 || m.Closed.Database.Queries != 30 || m.Delta.Queries != 10 || m.Delta.TransactionsBegun != 2 || m.Delta.EmptyAcquireCount != 3 || m.Delta.EmptyAcquireWaitNS != 4 || m.After.Runtime.Goroutines != 42 || m.After.Runtime.HeapInuseBytes != 4096 || m.After.Pool.TotalConns != 3 || m.After.Pool.MaxConns != 8 {
		t.Fatalf("bad snapshots: %s", out.String())
	}
	if got.Generator.NOFILESoft == 0 || got.Generator.NOFILEHard < got.Generator.NOFILESoft || got.Generator.Goroutines == 0 || got.Generator.HeapInuseBytes == 0 || requests.Load() != 4 || got.TCPConnections < 3 || got.TCPConnections > 4 {
		t.Fatalf("bad generator: %s", out.String())
	}
	if got.Renders.Count != 2 || fmt.Sprint(got.Renders.SizesBytes) != "[12 5]" || got.Renders.MinBytes != 5 || got.Renders.MaxBytes != 12 || got.Renders.TotalBytes != 17 || got.Renders.MeanBytes != 8.5 {
		t.Fatalf("bad renders: %s", out.String())
	}
}

func TestPaddingAndDialFailures(t *testing.T) {
	for _, length := range []int{0, 1, 40, 4000} {
		for _, escape := range []bool{false, true} {
			body := paddedBody("markerZ", length, escape)
			if !strings.HasPrefix(body, "markerZ") || len(body) != max(length, len("markerZ")) {
				t.Fatal("padding lost marker or length")
			}
		}
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{syscall.EMFILE, 0}, {syscall.ENFILE, 0}, {syscall.EADDRNOTAVAIL, 1}, {syscall.EADDRINUSE, 1}, {syscall.ECONNREFUSED, 2}, {context.DeadlineExceeded, 3}, {syscall.EINVAL, 4}} {
		if got := dialFailure(fmt.Errorf("dial: %w", tc.err)); got != tc.want {
			t.Errorf("%v: class %d, want %d", tc.err, got, tc.want)
		}
	}
}

// Identical local IPs work on macOS too; the selection counter proves that
// each concurrent dial takes exactly one slot even when peers look alike.
func TestSourceDials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	c := &counts{sources: []*net.TCPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("127.0.0.1")}}}
	tr, err := newTransport(server.URL, "", c)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			conn, err := tr.DialContext(t.Context(), "tcp", tr.origin.Host)
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		})
	}
	wg.Wait()
	if c.nextSource.Load() != 8 || c.tcp.Load() != 8 {
		t.Fatal("lost concurrent source selections")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if conn, err := tr.DialContext(t.Context(), "tcp", address); err == nil {
		_ = conn.Close()
		t.Fatal("dial to closed listener succeeded")
	}
	if c.dialFailures[2].Load() != 1 {
		t.Fatal("refused dial not counted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tr.DialContext(ctx, "tcp", address); err == nil || c.dialFailures[2].Load() != 1 {
		t.Fatal("cancelled dial succeeded or counted as refused")
	}
	for _, i := range []int{0, 1, 3, 4} {
		if c.dialFailures[i].Load() != 0 {
			t.Fatal("cancelled dial counted")
		}
	}
	c.sources[1].IP = net.ParseIP("127.0.0.2")
	c.nextSource.Store(0)
	for i := range 4 {
		conn, err := tr.DialContext(t.Context(), "tcp", tr.origin.Host)
		if i == 1 && errors.Is(err, syscall.EADDRNOTAVAIL) {
			t.Skip("127.0.0.2 requires a loopback alias")
		}
		if err != nil {
			t.Fatal(err)
		}
		local := conn.LocalAddr().(*net.TCPAddr).IP
		_ = conn.Close()
		if !local.Equal(c.sources[i%2].IP) {
			t.Fatalf("dial %d: source %s, want %s", i, local, c.sources[i%2].IP)
		}
	}
}

func TestReconnect(t *testing.T) {
	for _, cut := range []string{"503", "refused", "other", "id: 9\n", "id: 9\nevent: message\ndata: first\ndata: sec", "", "reset"} {
		t.Run(cut, func(t *testing.T) {
			var requests, dials atomic.Int64
			var first time.Time
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				want := "7"
				if n > 1 && cut != "503" && cut != "other" {
					want = "8"
				}
				if r.Header.Get("Last-Event-ID") != want {
					t.Errorf("cursor: %s, want %s", r.Header.Get("Last-Event-ID"), want)
				}
				if n <= 2 && cut == "503" {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if cut != "reset" {
					_, _ = fmt.Fprint(w, "id: 8\nevent: message\ndata: loadgenTEST0Z\n\n")
				}
				if n == 1 && cut != "503" && cut != "refused" && cut != "other" && cut != "reset" {
					_, _ = fmt.Fprint(w, cut)
					return
				}
				_, _ = fmt.Fprint(w, "event: reset\ndata:\n\n")
			}))
			defer server.Close()
			c := &counts{markers: regexp.MustCompile(`loadgen[A-Z0-9]+Z`)}
			tr, err := newTransport(server.URL, "", c)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.CloseIdleConnections()
			dial := tr.DialContext
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					first = time.Now()
					if cut == "refused" || cut == "other" {
						if cut == "other" {
							return nil, syscall.EINVAL
						}
						return nil, syscall.ECONNREFUSED
					}
				}
				if cut == "refused" && time.Since(first) < 5*time.Millisecond {
					t.Error("refused dial retried before delay")
				}
				return dial(ctx, network, address)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			slots := make(chan struct{}, 1)
			slots <- struct{}{}
			rec := streamRecord{Sequences: make(map[uint64]*receipt)}
			stream(ctx, &http.Client{Transport: tr}, server.URL+"/events", "secret", "7", c, ctx, slots, func() { <-slots }, &reconnectModel{Delay: 5 * time.Millisecond}, &rec)
			arrivals := uint64(2)
			if cut == "503" || cut == "refused" || cut == "other" {
				arrivals = 1
			}
			if rec.Reset != 1 || rec.Sequences[9] != nil || ctx.Err() != nil {
				t.Fatalf("bad termination: %+v", rec)
			}
			if cut == "reset" {
				if requests.Load() != 1 || len(rec.Sequences) != 0 || rec.Reconnects.Established != 0 {
					t.Fatal("reset reconnected")
				}
			} else if rec.Sequences[8] == nil || rec.Sequences[8].Arrivals != arrivals || rec.Sequences[8].Marker != "loadgenTEST0Z" || rec.Reconnects.Established != 1 {
				t.Fatalf("bad receipts: %+v", rec)
			}
			if cut == "503" && (c.connectionAttempts[1].Load() != 2 || rec.Reconnects.Unavailable != 1) || cut == "refused" && c.connectionAttempts[2].Load() != 1 || cut == "other" && c.connectionAttempts[3].Load() != 1 {
				t.Fatal("attempt outcome lost")
			}
		})
	}
}

func TestReceiptsAndPostRetry(t *testing.T) {
	var attempts, pages atomic.Int64
	events := make(chan string, 1)
	var marker string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			body := r.FormValue("body")
			if attempts.Add(1) == 1 {
				marker = body
				w.WriteHeader(503)
				return
			}
			if body != marker {
				t.Error("retry changed marker")
			}
			events <- body
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/events") {
			cursor := 7
			if pages.Add(1) > 1 {
				cursor = 99
			}
			_, _ = fmt.Fprintf(w, `<div sse-connect="/events?after=%d"></div>`, cursor)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_ = http.NewResponseController(w).Flush()
		cookie, _ := r.Cookie("__Host-session")
		if cookie.Value == "secret" {
			select {
			case body := <-events:
				for range 2 {
					_, _ = fmt.Fprintf(w, "id: 8\nevent: message\ndata: %s\n\n", body)
				}
			case <-r.Context().Done():
				return
			}
		}
		_, _ = fmt.Fprint(w, "event: reset\ndata:\n\n")
	}))
	defer server.Close()
	dir := t.TempDir()
	tokens, path := dir+"/tokens.json", dir+"/receipts.json"
	if err := os.WriteFile(tokens, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret","empty"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-target", server.URL, "-tokens", tokens, "-streams=2", "-duration=30ms", "-rate=1", "-drain=10ms", "-reconnect", "-receipts", path}
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"final_watermark":99`, `"sequences":{}`, `"reset":1`, `"reconnects":{"established":0,"503":0,"refused":0,"other":0}`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Fatalf("missing required receipt field: %s", field)
		}
	}
	var got runFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || got.Header != (runHeader{1, "test", "one", 7, 99}) || len(got.Streams) != 2 || got.Streams[0].Index != 0 || got.Streams[1].Index != 1 || len(got.Streams[1].Sequences) != 0 || got.Streams[0].Sequences[8] == nil || *got.Streams[0].Sequences[8] != (receipt{2, marker}) || got.Streams[1].Reset != 1 || strings.Contains(string(raw), "secret") {
		t.Fatalf("bad file: %s", raw)
	}
	var step result
	if err := json.Unmarshal(out.Bytes(), &step); err != nil {
		t.Fatal(err)
	}
	if step.Sent != 1 || step.Answered200 != 1 || step.PostFailed != 0 || step.PostAttemptsMade != 2 || step.Reconnect.Attempts.Established != 2 || step.Reconnect.Delay != 250*time.Millisecond || step.Reconnect.Jitter != 250*time.Millisecond || step.Reconnect.PostAttempts != 3 {
		t.Fatalf("bad retry counts: %s", out.String())
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("overwrote receipts")
	}
}

func TestRetryLimits(t *testing.T) {
	for range 100 {
		if got := retryDelay(5*time.Millisecond, 3*time.Millisecond); got < 5*time.Millisecond || got > 8*time.Millisecond || retryDelay(5*time.Millisecond, 0) != 5*time.Millisecond {
			t.Fatal("retry is not fixed delay plus bounded jitter")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	c := &counts{}
	if sendPost(t.Context(), server.Client(), server.URL, "secret", "loadgenTEST0Z", c, &reconnectModel{PostAttempts: 3}) || c.postAttempts.Load() != 3 {
		t.Fatal("POST did not exhaust exactly three attempts")
	}
	tokens := t.TempDir() + "/tokens.json"
	if err := os.WriteFile(tokens, []byte(`{"organization_slug":"test","channel_ids":["one"],"accounts":[{"tokens":["secret"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-target", server.URL, "-tokens", tokens, "-cursor=7", "-duration=1ms", "-rate=1", "-reconnect-delay=0"}
	var out bytes.Buffer
	var step result
	if err := run(args, &out); err != nil || json.Unmarshal(out.Bytes(), &step) != nil || step.Sent != 1 || step.PostFailed != 1 || step.Answered200 != 0 || step.PostAttemptsMade != 3 {
		t.Fatalf("bad exhausted POST counts: %v: %s", err, out.String())
	}
	if err := run(append(args, "-receipts", t.TempDir()+"/receipts.json"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "channel cursor") {
		t.Fatalf("missing watermark accepted: %v", err)
	}
	for _, flags := range [][]string{nil, {"-reconnect-delay=0", "-reconnect-jitter=0", "-post-attempts=1"}, {"-reconnect-delay=10s", "-reconnect-jitter=10s", "-post-attempts=10"}} {
		if err := run(flags, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "credential file") {
			t.Fatalf("valid flags rejected: %v: %v", flags, err)
		}
	}
}
