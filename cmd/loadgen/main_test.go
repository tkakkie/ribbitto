package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	for _, args := range [][]string{{"-duration=0"}, {"-duration=11m"}, {"-streams=0"}, {"-streams=10001"}, {"-rate=-1"}, {"-rate=101"}} {
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
					if r.FormValue("body") != "load test" || r.Header.Get("Origin") == "" || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
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
			if err := run([]string{"-target", server.URL, "-tokens", path, "-cursor=7", "-duration=100ms", "-rate=50"}, &out); err != nil {
				t.Fatal(err)
			}
			want := map[int]string{200: "established=1 refused_429=0 refused_503=0 reset=1 failed=0", 429: "refused_429=1", 503: "refused_503=1", 400: "failed=1"}[status]
			if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), " posts=0 ") {
				t.Fatal(out.String())
			}
		})
	}
	for _, body := range []string{"event: reset\n\n", "event: reset\ndata: {}", "event: reset\nevent: message\ndata: {}\n\n"} {
		if readReset(strings.NewReader(body)) {
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
