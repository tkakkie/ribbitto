package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func defaultLoggedHandler(t *testing.T, logs *bytes.Buffer, override func(*Services)) http.Handler {
	t.Helper()
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler("", catalogues, testServices(override))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func logRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestRequestErrorLogID(t *testing.T) {
	failure := errors.New("unavailable")
	for _, tt := range []struct {
		name, method, path, message, extra string
		override                           func(*Services)
	}{
		{"serverError", "POST", "/signin", "signing in", "err", func(s *Services) { s.SignIn = &fakeSignIn{err: failure} }},
		{"session", "GET", "/signin", "resolving session", "err", func(s *Services) { s.Sessions = &countingResolver{} }},
		{"readiness", "GET", "/readyz", "readiness database ping failed", "", func(s *Services) {
			s.Database = fakePinger(func(context.Context) error { return failure })
		}},
		{"horizon", "GET", "/signin", "reading the drafts horizon", "err", func(s *Services) { s.SignIn = &fakeSignIn{horizonErr: failure} }},
		{"streamStart", "GET", "/organizations/acme/events?after=0&want=sidebar&channel=" + streamChannel, "starting event stream", "err", nil},
		{"streamStop", "GET", "/organizations/acme/events?after=0&want=sidebar&channel=" + streamChannel, "event stream stopped", "cursor,err", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var previousID string
			for range 2 {
				var logs bytes.Buffer
				h := defaultLoggedHandler(t, &logs, func(s *Services) {
					if tt.override != nil {
						tt.override(s)
						return
					}
					asAlice(s)
					s.Stream = &Streaming{Lifetime: t.Context(), Hub: realtime.NewHub(), Events: noEvents{}, Authorizer: streamAllow(true), Sessions: oneSession{}}
				})
				r := httptest.NewRequest(tt.method, tt.path, nil)
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
				if tt.name == "streamStart" {
					w.flushErr = failure
				}
				h.ServeHTTP(w, r)
				records := logRecords(t, &logs)
				if len(records) != 2 || records[0]["msg"] != tt.message || records[1]["msg"] != "request" {
					t.Fatalf("unexpected logs: %s", logs.String())
				}
				id, _ := records[1]["request_id"].(string)
				if id == "" || id == previousID || records[0]["request_id"] != id {
					t.Fatalf("IDs do not match or repeat: %v, previous %q", records, previousID)
				}
				previousID = id
				allowed := append([]string{"time", "level", "msg", "request_id"}, strings.Split(tt.extra, ",")...)
				for key := range records[0] {
					if !slices.Contains(allowed, key) {
						t.Fatalf("unexpected error field %s", key)
					}
				}
			}
		})
	}
}

func TestRequestErrorLogPrivacy(t *testing.T) {
	var logs bytes.Buffer
	h := defaultLoggedHandler(t, &logs, func(s *Services) { s.SignIn = &fakeSignIn{err: errors.New("unavailable")} })
	r := httptest.NewRequest("POST", "/signin?query=query-canary", strings.NewReader("password=password-canary&email=email-canary@example.invalid&body=message-canary"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "session-canary"})
	r.Header.Set("X-Forwarded-For", "198.51.100.77")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	records := logRecords(t, &logs)
	if w.Code != 500 || len(records) != 2 || records[0]["msg"] != "signing in" {
		t.Fatalf("did not exercise serverError: status %d, logs %s", w.Code, logs.String())
	}
	for _, input := range []string{"canary", "198.51.100.77", r.RemoteAddr} {
		if strings.Contains(logs.String(), input) {
			t.Fatalf("logged sensitive input: %s", logs.String())
		}
	}
}
