package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func loggedHandler(t *testing.T, logs *bytes.Buffer, overrides ...func(*Services)) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	catalogues, err := i18n.New(logger)
	if err != nil {
		t.Fatal(err)
	}
	s := testServices(overrides...)
	s.RequestLogger = logger
	h, err := NewHandler("", catalogues, s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func requestRecord(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	if strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("want one log line, got %s", logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRequestLogFields(t *testing.T) {
	var logs bytes.Buffer
	h := loggedHandler(t, &logs)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("HEAD", "/healthz", nil))
	r := requestRecord(t, &logs)
	for name, want := range map[string]any{"method": "HEAD", "status": float64(200), "msg": "request"} {
		t.Run(name, func(t *testing.T) {
			if r[name] != want {
				t.Fatalf("%s = %v, want %v", name, r[name], want)
			}
		})
	}
	t.Run("duration", func(t *testing.T) {
		if d, ok := r["duration"].(float64); !ok || d <= 0 {
			t.Fatalf("duration = %v", r["duration"])
		}
	})
}

func TestRequestLogRoutes(t *testing.T) {
	for _, tt := range []struct {
		method, path, route string
		status              int
	}{
		{"GET", "/organizations/private/channels/private", "GET /organizations/{slug}/channels/{channelID}", 404},
		{"GET", "/missing/private?private=value", "unmatched", 404},
		{"DELETE", "/signin", "unmatched", 405},
		{"GET", "/healthz", "GET /healthz", 200},
		{"GET", "/readyz", "GET /readyz", 503},
		{"GET", "/static/missing", "/static/", 404},
		{"GET", "/setup", "GET /setup", 200},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			var logs bytes.Buffer
			h := loggedHandler(t, &logs, func(s *Services) { s.Setup, s.SetupSessions = &fakeSetup{open: true}, &fakeSetup{} })
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			r := requestRecord(t, &logs)
			if r["route"] != tt.route || r["status"] != float64(tt.status) || w.Code != tt.status {
				t.Fatalf("record = %v, response = %d", r, w.Code)
			}
		})
	}
}

func TestRequestLogID(t *testing.T) {
	var logs bytes.Buffer
	var contextID, previous string
	h := loggedHandler(t, &logs, func(s *Services) {
		s.Database = fakePinger(func(ctx context.Context) error { contextID = middleware.RequestID(ctx); return nil })
	})
	for range 2 {
		logs.Reset()
		r := httptest.NewRequest("GET", "/readyz", nil)
		r.Header.Set("X-Request-ID", "untrusted-client-id")
		h.ServeHTTP(httptest.NewRecorder(), r)
		id, _ := requestRecord(t, &logs)["request_id"].(string)
		if id == "" || id != contextID || id == previous || id == "untrusted-client-id" {
			t.Fatalf("invalid request ID %q (context %q, previous %q)", id, contextID, previous)
		}
		previous = id
	}
}

func TestRequestLogPrivacy(t *testing.T) {
	for _, tt := range []struct{ name, header, value, body, query string }{
		{name: "cookie", header: "Cookie", value: "__Host-session=session-canary"},
		{name: "password", body: "password=password-canary"},
		{name: "query", query: "?query=query-canary"},
		{name: "forwarded", header: "X-Forwarded-For", value: "198.51.100.77"},
		{name: "authorization", header: "Authorization", value: "Bearer authorization-canary"},
		{name: "email", body: "email=email-canary@example.invalid"},
		{name: "message", body: "body=message-canary"},
		{name: "token", body: "token=token-canary"},
		{name: "combined", header: "Cookie", value: "__Host-session=session-canary", body: "password=password-canary", query: "?query=query-canary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := loggedHandler(t, &logs)
			r := httptest.NewRequest("POST", "/signin"+tt.query, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.header != "" {
				r.Header.Set(tt.header, tt.value)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			for _, part := range []string{tt.value, tt.body, tt.query, "canary", r.RemoteAddr} {
				if part != "" && strings.Contains(logs.String(), part) {
					t.Fatalf("logged sensitive input: %s", logs.String())
				}
			}
			for key := range requestRecord(t, &logs) {
				if !slices.Contains([]string{"time", "level", "msg", "request_id", "method", "route", "status", "duration"}, key) {
					t.Fatalf("unexpected field %s", key)
				}
			}
		})
	}
}

func TestRequestLogEarlyRejection(t *testing.T) {
	var logs bytes.Buffer
	h := loggedHandler(t, &logs)
	r := httptest.NewRequest("POST", "/signin", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if record := requestRecord(t, &logs); record["status"] != float64(403) || record["route"] != "unmatched" {
		t.Fatal(record)
	}
}

func TestRequestLogOversizedBodyClosesConnection(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(loggedHandler(t, &logs))
	defer server.Close()
	resp, err := server.Client().Post(server.URL+"/signin", "application/x-www-form-urlencoded",
		strings.NewReader(strings.Repeat("x", middleware.MaxBodyBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// Wait for the server's handler goroutine before reading its log buffer.
	server.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Fatal("oversized body response did not close the connection")
	}
	if record := requestRecord(t, &logs); record["status"] != float64(413) || record["route"] != "POST /signin" {
		t.Fatal(record)
	}
}

func TestRequestLogStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		hub := realtime.NewHub()
		h := loggedHandler(t, &logs, asAlice, func(s *Services) {
			s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: quietLog{}, Authorizer: streamAllow(true), Sessions: oneSession{}, Heartbeat: time.Second}
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		r := httptest.NewRequest("GET", "/organizations/acme/events?after=0&want=sidebar&channel="+streamChannel, nil).WithContext(ctx)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() { defer close(done); h.ServeHTTP(w, r) }()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if logs.Len() != 0 {
			t.Fatalf("logged before stream ended: %s", logs.String())
		}
		cancel()
		<-done
		if strings.Count(w.Body.String(), ": heartbeat") < 2 {
			t.Fatalf("stream did not send multiple frames: %s", w.Body.String())
		}
		if record := requestRecord(t, &logs); record["route"] != "GET /organizations/{slug}/events" || record["status"] != float64(200) || record["duration"].(float64) < float64(3*time.Second) {
			t.Fatal(record)
		}
		for _, call := range []string{"set", "flush", "clear"} {
			if !slices.Contains(w.calls, call) {
				t.Fatalf("ResponseController did not reach underlying writer: %v", w.calls)
			}
		}
	})
}
