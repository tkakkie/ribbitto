package web

import (
	"context"
	"errors"
	"fmt"
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

func TestStreamRecountAdmission(t *testing.T) {
	for _, tt := range []struct {
		name, mode, interests string
		status, opens         int
		recounts              int32
	}{
		{"capacity", "full", "sidebar", 503, 0, 0},
		{"closure", "closed", "sidebar", 503, 0, 0},
		{"session missing at recheck", "missing", "sidebar", 404, 0, 0},
		{"session lookup failed", "lookup", "sidebar", 500, 0, 0},
		{"session ends after reservation", "session", "sidebar", 404, 1, 0},
		{"request ends after reservation", "request", "sidebar", 404, 1, 0},
		{"initial write fails", "write", "sidebar", 200, 1, 0},
		{"accepted", "accepted", "sidebar", 200, 1, 1},
		{"messages never reserve", "full", "messages", 200, 1, 0},
		{"typing never reserves", "full", "typing", 200, 1, 0},
		{"presence never reserves", "full", "presence&presence-after=instance:0", 200, 1, 0},
		{"unwired never reserves", "unwired", "sidebar", 200, 1, 0},
		{"nil source never reserves", "source", "sidebar", 200, 1, 0},
		{"nil scheduler", "nil", "sidebar", 200, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			q := realtime.NewRecountScheduler(1, 1)
			t.Cleanup(q.Close)
			var occupied *realtime.RecountRegistration
			if tt.mode == "full" {
				occupied = q.Register()
				t.Cleanup(occupied.Cancel)
			}
			if tt.mode == "closed" {
				q.Close()
			}
			live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}
			seen, opens, closes, factories := 0, 0, 0, 0
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			var reads atomic.Int32
			source := &admissionSidebar{}
			observer := &realtime.RecountObserver{}
			stream := &Streaming{RecountObserver: observer, Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Authorizer: streamAllow(true), Recounts: q}
			stream.Sessions = admissionSession(func(context.Context, string) (identity.Account, identity.Session, error) {
				seen++
				want := 0
				if occupied != nil {
					want = 1
				}
				if hub.Connections() != 1 || q.Stats().Registrations != want {
					t.Fatal("session recheck must follow hub registration and precede recount admission")
				}
				switch tt.mode {
				case "missing":
					return identity.Account{}, identity.Session{}, identity.ErrNoSession
				case "lookup":
					return identity.Account{}, identity.Session{}, errors.New("session store unavailable")
				}
				return identity.Account{ID: kernel.ID{1}}, live, nil
			})
			stream.Sidebar = func(org.Membership, kernel.ID, *kernel.ID, bool) realtime.SidebarSource {
				factories++
				if seen != 1 {
					t.Fatal("sidebar source created before session recheck")
				}
				if tt.interests == "sidebar" && (opens == 0) != (stream.Recounts != nil) {
					t.Fatal("sidebar source timing changed: nil scheduler must bind after opening")
				}
				if tt.mode == "nil" {
					w.mu.Lock()
					defer w.mu.Unlock()
					if len(w.calls) != 3 || w.calls[1] != "flush" {
						t.Fatal("nil scheduler must bind the source after the initial SSE flush")
					}
				}
				if tt.mode == "source" {
					return nil
				}
				return source
			}
			reserved := tt.interests == "sidebar" && tt.mode != "unwired" && tt.mode != "source" && tt.mode != "nil"
			stream.StreamOpened = func(kernel.ID, kernel.ID, kernel.ID) func() {
				opens++
				want := 0
				if reserved || occupied != nil {
					want = 1
				}
				if hub.Connections() != 1 || q.Stats().Registrations != want {
					t.Fatal("presence/typing opened before recount reservation")
				}
				return func() { closes++ }
			}
			if tt.mode == "unwired" {
				stream.Sidebar = nil
			}
			if tt.mode == "nil" {
				stream.Recounts = nil
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
					if q.Stats().Registrations != 1 {
						t.Fatal("cancellation must happen after recount reservation")
					}
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
			if tt.status == 503 && (w.Body.String() != "Service Unavailable\n" || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("scheduler refusal = %v, %q", w.Header(), w.Body.String())
			}
			if opens != tt.opens || closes != tt.opens || seen != 1 {
				t.Fatalf("presence/typing opens=%d closes=%d, rechecks=%d; want %d/%d/1", opens, closes, seen, tt.opens, tt.opens)
			}
			if (tt.mode == "missing" || tt.mode == "lookup") && factories != 0 {
				t.Fatal("failed session recheck reached recount admission")
			}
			if tt.status != 200 || tt.mode == "write" {
				if reads.Load() != 0 {
					t.Fatal("failed admission or initial write read events")
				}
			}
			if n := source.recounts.Load(); n != tt.recounts {
				t.Fatalf("recounts = %d; want %d (no synchronous fallback)", n, tt.recounts)
			}
			stats := q.Stats()
			refusals := uint64(0)
			if (tt.mode == "full" && tt.interests == "sidebar") || tt.mode == "closed" {
				refusals = 1
			}
			if stats.RefusedCapacity+stats.RefusedClosed != refusals || (stats.RefusedClosed != 0) != (tt.mode == "closed") {
				t.Fatalf("unexpected scheduler admission: %+v", stats)
			}
			wantObserved := uint64(0)
			if tt.mode == "accepted" {
				wantObserved = 1
			}
			if got := observer.Stats(); got.Submitted != wantObserved || got.Received != wantObserved || got.Failed != wantObserved || got.Delivered != 0 {
				t.Fatalf("web observation forwarding: %+v", got)
			}
			if tt.mode == "accepted" && stats.Completed != 1 {
				t.Fatalf("accepted stream did not use reserved scheduler: %+v", stats)
			}
			if occupied != nil {
				if stats.Registrations != 1 {
					t.Fatal("stream released someone else's reservation or retained its own")
				}
				occupied.Cancel()
			}
			if hub.Connections() != 0 || q.Stats().Registrations != 0 {
				t.Fatalf("stream retained hub=%d scheduler=%d registrations", hub.Connections(), q.Stats().Registrations)
			}
		})
	}
}

// Cancellation can win during admission itself, before presence/typing opens.
func TestOpenStreamRecountCancellation(t *testing.T) {
	for _, sessionEnds := range []bool{false, true} {
		t.Run(fmt.Sprint("session=", sessionEnds), func(t *testing.T) {
			hub := realtime.NewHub()
			q := realtime.NewRecountScheduler(1, 1)
			defer q.Close()
			live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}
			seen := 0
			p := channelPages{stream: &Streaming{Hub: hub, Sessions: laterSession{hub: hub, session: live, seen: &seen}, StreamOpened: func(kernel.ID, kernel.ID, kernel.ID) func() {
				t.Fatal("presence/typing opened after cancellation during admission")
				return nil
			}}}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(parent)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			var registration *realtime.RecountRegistration
			ctx, cleanup, ok := p.openStream(w, r, kernel.ID{9}, kernel.ID{1}, kernel.ID{3}, kernel.ID{7}, live, func() bool {
				registration = q.Register()
				if seen != 1 || registration == nil {
					t.Fatal("admission must follow session recheck")
				}
				t.Cleanup(registration.Cancel)
				if sessionEnds {
					hub.CancelSession(live.ID)
				} else {
					cancel()
				}
				return true
			})
			registration.Cancel() // the caller owns failed admission
			if ok || ctx != nil || cleanup != nil || w.Code != 404 || hub.Connections() != 0 || q.Stats().Registrations != 0 {
				t.Fatalf("cancelled admission: ok=%t status=%d hub=%d scheduler=%d", ok, w.Code, hub.Connections(), q.Stats().Registrations)
			}
		})
	}
}
