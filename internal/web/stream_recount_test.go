package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

type admissionSidebar struct{ recounts atomic.Int32 }

func (*admissionSidebar) Trigger(realtime.Event) bool { return false }
func (s *admissionSidebar) Recount(context.Context, realtime.Subscription) (realtime.Outgoing, error) {
	s.recounts.Add(1)
	return realtime.Outgoing{}, errors.New("stop after recount")
}

type admissionSession func(context.Context, string) (identity.Account, identity.Session, error)

func (s admissionSession) Resolve(ctx context.Context, token string) (identity.Account, identity.Session, error) {
	return s(ctx, token)
}

func TestStreamSynchronousSidebar(t *testing.T) {
	for _, tt := range []struct {
		name, mode, interests string
		status, opens         int
		recounts              int32
	}{
		{"session missing at recheck", "missing", "sidebar", 404, 0, 0},
		{"session lookup failed", "lookup", "sidebar", 500, 0, 0},
		{"session ends after opening", "session", "sidebar", 404, 1, 0},
		{"request ends after opening", "request", "sidebar", 404, 1, 0},
		{"initial write fails", "write", "sidebar", 200, 1, 0},
		{"messages", "messages", "messages", 200, 1, 0},
		{"typing", "typing", "typing", 200, 1, 0},
		{"presence", "presence", "presence&presence-after=instance:0", 200, 1, 0},
		{"unwired", "unwired", "sidebar", 200, 1, 0},
		{"nil source", "source", "sidebar", 200, 1, 0},
		{"synchronous sidebar", "sync", "sidebar", 200, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}
			seen, opens, closes, factories := 0, 0, 0, 0
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			var reads atomic.Int32
			source := &admissionSidebar{}
			stream := &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Authorizer: streamAllow(true)}
			stream.Sessions = admissionSession(func(context.Context, string) (identity.Account, identity.Session, error) {
				seen++
				if hub.Connections() != 1 {
					t.Fatal("session recheck must follow hub registration")
				}
				switch tt.mode {
				case "missing":
					return identity.Account{}, identity.Session{}, identity.ErrNoSession
				case "lookup":
					return identity.Account{}, identity.Session{}, errors.New("session store unavailable")
				}
				return identity.Account{ID: kernel.ID{1}}, live, nil
			})
			stream.Sidebar = func(org.Membership, kernel.ID, *kernel.ID) realtime.SidebarSource {
				factories++
				if seen != 1 {
					t.Fatal("sidebar source created before session recheck")
				}
				if opens != 1 {
					t.Fatal("sidebar source must bind after opening")
				}
				w.mu.Lock()
				defer w.mu.Unlock()
				if len(w.calls) != 3 || w.calls[1] != "flush" {
					t.Fatal("sidebar source must bind after the initial SSE flush")
				}
				if tt.mode == "source" {
					return nil
				}
				return source
			}
			stream.StreamOpened = func(kernel.ID, kernel.ID, kernel.ID) func() {
				opens++
				if hub.Connections() != 1 || seen != 1 {
					t.Fatal("presence/typing must open after registration and session recheck")
				}
				return func() { closes++ }
			}
			if tt.mode == "unwired" {
				stream.Sidebar = nil
			}
			if tt.recounts != 0 {
				stream.Events = quietLog{}
			}
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Stream = stream }))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/organizations/acme/events?after=0&channel="+streamChannel+"&want="+tt.interests, nil).WithContext(ctx)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			var writer http.ResponseWriter = w
			if tt.mode == "write" {
				writer = w.ResponseRecorder // no write-deadline support
			}
			if tt.mode == "session" || tt.mode == "request" {
				writer = &cancellingWriter{deadlineWriter: w, hub: hub, cancel: func() {
					if tt.mode == "session" {
						hub.CancelSession(live.ID)
					} else {
						cancel()
					}
				}}
			}
			handler.ServeHTTP(writer, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, body %q; want %d", w.Code, w.Body.String(), tt.status)
			}
			if tt.status != 200 && (w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Body.Len() == 0) {
				t.Fatalf("refusal committed SSE headers or lost plain-text response: %v, %q", w.Header(), w.Body.String())
			}
			if opens != tt.opens || closes != tt.opens || seen != 1 {
				t.Fatalf("presence/typing opens=%d closes=%d, rechecks=%d; want %d/%d/1", opens, closes, seen, tt.opens, tt.opens)
			}
			if (tt.mode == "missing" || tt.mode == "lookup") && factories != 0 {
				t.Fatal("failed session recheck created sidebar source")
			}
			if tt.status != 200 || tt.mode == "write" {
				if reads.Load() != 0 {
					t.Fatal("failed opening or initial write read events")
				}
			}
			if n := source.recounts.Load(); n != tt.recounts {
				t.Fatalf("recounts = %d; want %d", n, tt.recounts)
			}
			if hub.Connections() != 0 {
				t.Fatalf("stream retained %d hub registrations", hub.Connections())
			}
		})
	}
}
