package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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
	// /large is bigger than the largest static asset, for a client that
	// reads promptly.
	handler.HandleFunc("GET /large", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(largeBody)
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

// largeBody is 256 KiB, five times the largest static asset (htmx).
var largeBody = bytes.Repeat([]byte("0123456789abcdef"), 16<<10)

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
	// The write timeout is longer than the others: the reading client below
	// must never be cut off by scheduling noise.
	timeouts := serverTimeouts{readHeader: 200 * time.Millisecond, read: 300 * time.Millisecond, idle: 300 * time.Millisecond, write: 2 * time.Second}
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
			"GET /large HTTP/1.1\r\nHost: x\r\n\r\n",
		} {
			send(t, conn, request)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatalf("%q: reading body: %v", request, err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%q: status %d", request, response.StatusCode)
			}
			if strings.Contains(request, "/large") && !bytes.Equal(body, largeBody) {
				t.Fatalf("/large: got %d bytes, want the whole %d-byte body", len(body), len(largeBody))
			}
		}
	})
}

// TestServerWriteDeadline sends a request and never reads the response. The
// server must give up on the write, let the handler return and close the
// connection, rather than hold them for as long as the client likes.
func TestServerWriteDeadline(t *testing.T) {
	// Generous against scheduling noise, far below "never".
	const limit = 3 * time.Second
	written := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 64 MiB: far more than the socket buffers of both ends hold.
		chunk := make([]byte, 64<<10)
		var err error
		for i := 0; i < 1024 && err == nil; i++ {
			_, err = w.Write(chunk)
		}
		written <- err
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer("", handler, serverTimeouts{
		readHeader: time.Second, read: time.Second, idle: time.Second,
		write: 200 * time.Millisecond,
	})
	closed := make(chan struct{})
	var once sync.Once
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			once.Do(func() { close(closed) })
		}
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn := dial(t, listener.Addr().String())
	send(t, conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	// One deadline for both observations.
	deadline := time.After(limit)
	select {
	case err := <-written:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write error = %v, want a deadline error", err)
		}
	case <-deadline:
		t.Fatal("the handler was still writing to a client that never reads")
	}
	select {
	case <-closed:
	case <-deadline:
		t.Fatal("the connection to a client that never reads was never closed")
	}
}

// TestServerReadHeaderTimeout stalls before the headers are complete. The
// other limits are far longer than the observation window, so only
// ReadHeaderTimeout can close the connection in time.
func TestServerReadHeaderTimeout(t *testing.T) {
	addr := startServer(t, serverTimeouts{
		readHeader: 200 * time.Millisecond, read: time.Minute, idle: time.Minute, write: time.Minute,
	})
	conn := dial(t, addr)
	send(t, conn, "GET /healthz HTTP/1.1\r\nHost: x\r\n")
	if !closedWithin(t, conn, 3*time.Second) {
		t.Fatal("a connection with incomplete headers was never closed")
	}
}
