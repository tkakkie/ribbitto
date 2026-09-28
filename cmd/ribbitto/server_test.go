package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// startServer serves handler with newServer on a real listener, with short
// timeouts, and returns its address.
func startServer(t *testing.T, timeouts serverTimeouts) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.NewServeMux()
	handler.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	// /read reads the body, like a form handler; /refuse answers without
	// reading it, like a closed route or a rate-limited form.
	handler.HandleFunc("POST /read", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "read")
	})
	handler.HandleFunc("POST /refuse", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	})
	srv := newServer("", handler, timeouts)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return listener.Addr().String()
}

// closedWithin reports whether the server closes conn before limit,
// reading and discarding whatever it sends until then.
func closedWithin(t *testing.T, conn net.Conn, limit time.Duration) bool {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(limit)); err != nil {
		t.Fatal(err)
	}
	_, err := io.Copy(io.Discard, conn)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	return true
}

func send(t *testing.T, conn net.Conn, request string) {
	t.Helper()
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestServerTimeouts(t *testing.T) {
	timeouts := serverTimeouts{readHeader: 200 * time.Millisecond, read: 300 * time.Millisecond, idle: 300 * time.Millisecond}
	addr := startServer(t, timeouts)
	// Generous against scheduling noise, far below "never".
	const limit = 3 * time.Second

	t.Run("idle keep-alive", func(t *testing.T) {
		conn := dial(t, addr)
		send(t, conn, "GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n")
		if !closedWithin(t, conn, limit) {
			t.Fatal("an idle keep-alive connection was never closed")
		}
	})
	for _, path := range []string{"/read", "/refuse"} {
		t.Run("stalled body "+path, func(t *testing.T) {
			conn := dial(t, addr)
			send(t, conn, fmt.Sprintf("POST %s HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 1000\r\n\r\na", path))
			if !closedWithin(t, conn, limit) {
				t.Fatal("a connection with a stalled body was never closed")
			}
		})
	}
	t.Run("complete requests are served", func(t *testing.T) {
		conn := dial(t, addr)
		reader := bufio.NewReader(conn)
		for _, request := range []string{
			"POST /read HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 3\r\n\r\na=b",
			"GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n",
		} {
			send(t, conn, request)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%q: status %d", request, response.StatusCode)
			}
		}
	})
}
