package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestStreamCursor(t *testing.T) {
	tests := []struct {
		name, query, header string
		want                int64
		ok                  bool
	}{
		{name: "after", query: "?after=42", want: 42, ok: true},
		{name: "zero", query: "?after=0", want: 0, ok: true},
		{name: "header wins over after", query: "?after=1", header: "9", want: 9, ok: true},
		{name: "header without after", header: "9", want: 9, ok: true},
		{name: "malformed header does not fall back", query: "?after=1", header: "x", ok: false},
		{name: "negative header", header: "-1", ok: false},
		{name: "missing", ok: false},
		{name: "empty after", query: "?after=", ok: false},
		{name: "duplicate after", query: "?after=1&after=2", ok: false},
		{name: "negative after", query: "?after=-1", ok: false},
		{name: "not a number", query: "?after=x", ok: false},
		{name: "overflow", query: "?after=9223372036854775808", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/events"+tt.query, nil)
			if tt.header != "" {
				r.Header.Set("Last-Event-ID", tt.header)
			}
			got, ok := streamCursor(r)
			if ok != tt.ok || (ok && got != tt.want) {
				t.Fatalf("streamCursor = %d, %t; want %d, %t", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// laterSession answers the stream's second look at the session. It records
// how many connections the hub held at that moment (the stream must already
// be registered), and can end the session right then, as a sign-out landing
// just after registration would.
type laterSession struct {
	hub       *realtime.Hub
	session   identity.Session
	err       error
	cancelNow bool
	shutdown  bool
	seen      *int
}

func (l laterSession) Resolve(context.Context, string) (identity.Account, identity.Session, error) {
	*l.seen = l.hub.Connections()
	if l.cancelNow {
		l.hub.CancelSession(l.session.ID)
	}
	if l.shutdown {
		l.hub.CancelAll()
	}
	return identity.Account{ID: kernel.ID{1}}, l.session, l.err
}

// firstRead counts reads and fails each one, which ends a started stream.
type firstRead struct{ reads *atomic.Int32 }

func (f firstRead) EventsAfter(context.Context, kernel.ID, int64, int) ([]realtime.Event, error) {
	f.reads.Add(1)
	return nil, errors.New("stream started; stop after the first read")
}

// noEvents is an event log that is never read in these tests.
type noEvents struct{}

func (noEvents) EventsAfter(context.Context, kernel.ID, int64, int) ([]realtime.Event, error) {
	return nil, errors.New("the stream must not start")
}

// The middleware resolves the session, then it is deleted before the stream
// registers: nothing would cancel the stream, so the second look must stop it
// before anything is sent, and the registration must not outlive it.
func testStreamRechecksTheSessionAfterRegistering(t *testing.T) {
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	for _, tt := range []struct {
		name  string
		later laterSession
	}{
		{"session deleted", laterSession{err: identity.ErrNoSession}},
		{"session replaced by another", laterSession{session: identity.Session{ID: kernel.ID{0x77}, ExpiresAt: time.Now().Add(time.Hour)}}},
		// The session is still valid at the second look but ends at that
		// instant: only a registration made before it can be cancelled.
		{"signed out right after registering", laterSession{session: live, cancelNow: true}},
		// The sign-out lands while the second look is still querying: the
		// store wraps the cancelled context as an error, which must end the
		// stream like a deleted session, not as a server error.
		{"signed out during the second look", laterSession{session: live, cancelNow: true, err: fmt.Errorf("resolving session: %w", context.Canceled)}},
		{"shutdown after registering", laterSession{session: live, shutdown: true}},
		{"shutdown during the second look", laterSession{session: live, shutdown: true, err: fmt.Errorf("resolving session: %w", context.Canceled)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			seen := -1
			tt.later.hub, tt.later.seen = hub, &seen
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Authorizer: nil, Sessions: tt.later}
			}))
			if err != nil {
				t.Fatal(err)
			}
			w := serveForm(handler, http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", "live", nil)
			if seen != 1 {
				t.Fatalf("the second look saw %d registered connections, want 1: register before re-checking", seen)
			}
			if strings.Contains(w.Body.String(), "event:") || strings.Contains(w.Body.String(), "data:") {
				t.Fatalf("status %d, body %q; want nothing streamed", w.Code, w.Body.String())
			}
			want := http.StatusNotFound
			if tt.later.shutdown {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want || w.Body.Len() == 0 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("status %d, type %q, body %q; want a plain-text %d", w.Code, w.Header().Get("Content-Type"), w.Body.String(), want)
			}
			if reads.Load() != 0 {
				t.Fatal("cancelled stream read events")
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

// cancellingWriter cancels the stream the first time the handler touches
// the headers while the stream is registered: after the session re-check
// passed and before the status is committed. The middleware's earlier
// header writes happen before registration and do not count.
type cancellingWriter struct {
	*deadlineWriter
	hub    *realtime.Hub
	cancel func()
	fired  bool
}

func (w *cancellingWriter) Header() http.Header {
	if !w.fired && w.hub.Connections() == 1 {
		w.fired = true
		w.cancel()
	}
	return w.deadlineWriter.Header()
}

// The session re-check passed, and the stream is cancelled between it and
// the first write committing a status: the client still gets 404 (session
// ended) or 503 (shutdown), never an empty 200.
func testStreamCancelledBeforeTheFirstWrite(t *testing.T) {
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	for _, tt := range []struct {
		name   string
		cancel func(*realtime.Hub)
		want   int
	}{
		{"signed out", func(hub *realtime.Hub) { hub.CancelSession(live.ID) }, http.StatusNotFound},
		{"shutdown", func(hub *realtime.Hub) { hub.CancelAll() }, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			seen := -1
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Sessions: laterSession{hub: hub, session: live, seen: &seen}}
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := &cancellingWriter{deadlineWriter: &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}, hub: hub, cancel: func() { tt.cancel(hub) }}
			handler.ServeHTTP(w, r)
			if !w.fired || seen != 1 {
				t.Fatalf("cancelled %t, re-check saw %d connections; want the cancellation after a passed re-check", w.fired, seen)
			}
			if w.Code != tt.want || w.Body.Len() == 0 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("status %d, type %q, body %q; want a plain-text %d", w.Code, w.Header().Get("Content-Type"), w.Body.String(), tt.want)
			}
			if reads.Load() != 0 {
				t.Fatal("cancelled stream read events")
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

// cancellingConn is cancellingWriter for a real server.
type cancellingConn struct {
	http.ResponseWriter
	hub    *realtime.Hub
	cancel func()
	fired  atomic.Bool
}

func (w *cancellingConn) Header() http.Header {
	if w.hub.Connections() == 1 && w.fired.CompareAndSwap(false, true) {
		w.cancel()
	}
	return w.ResponseWriter.Header()
}

func (w *cancellingConn) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Over real connections, HTTP/1.1 and HTTP/2, the 404 or 503 for a stream
// cancelled before its status is committed reaches the client. Over HTTP/2
// an expired write deadline resets the stream, so nothing may expire it
// before the error is written.
func testStreamCancelledBeforeTheFirstWriteOverHTTP(t *testing.T) {
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	for _, tt := range []struct {
		name   string
		cancel func(*realtime.Hub)
		want   int
	}{
		{"signed out", func(hub *realtime.Hub) { hub.CancelSession(live.ID) }, http.StatusNotFound},
		{"shutdown", func(hub *realtime.Hub) { hub.CancelAll() }, http.StatusServiceUnavailable},
	} {
		for _, proto := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/HTTP%d", tt.name, proto), func(t *testing.T) {
				testCancelledStreamOverHTTP(t, live, tt.cancel, tt.want, proto)
			})
		}
	}
}

func testCancelledStreamOverHTTP(t *testing.T, live identity.Session, cancel func(*realtime.Hub), want, proto int) {
	t.Helper()
	hub := realtime.NewHub()
	seen := -1
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: noEvents{}, Sessions: laterSession{hub: hub, session: live, seen: &seen}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&cancellingConn{ResponseWriter: w, hub: hub, cancel: func() { cancel(hub) }}, r)
	}))
	server.EnableHTTP2 = proto == 2
	server.StartTLS()
	defer server.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("no response reached the client: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if resp.ProtoMajor != proto {
		t.Fatalf("negotiated HTTP/%d, want HTTP/%d", resp.ProtoMajor, proto)
	}
	if resp.StatusCode != want || len(body) == 0 || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status %d, type %q, body %q; want a plain-text %d", resp.StatusCode, resp.Header.Get("Content-Type"), body, want)
	}
	if seen != 1 {
		t.Fatalf("re-check saw %d connections; want the cancellation after a passed re-check", seen)
	}
}

func testStreamFetchSite(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values []string
		allow  bool
	}{
		{name: "absent", allow: true},
		{name: "same origin", values: []string{"same-origin"}, allow: true},
		{name: "same site", values: []string{"same-site"}},
		{name: "cross site", values: []string{"cross-site"}},
		{name: "none", values: []string{"none"}},
		{name: "empty", values: []string{""}},
		{name: "unknown", values: []string{"unknown"}},
		{name: "duplicate", values: []string{"same-origin", "cross-site"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}
			seen := -1
			var reads atomic.Int32
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Sessions: laterSession{hub: hub, session: live, seen: &seen}}
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			for _, value := range tt.values {
				r.Header.Add("Sec-Fetch-Site", value)
			}
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(w, r)
			if tt.allow {
				if w.Code != http.StatusOK || seen != 1 || reads.Load() != 1 || w.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
					t.Fatalf("status %d, registered %d, reads %d, type %q; want a started stream", w.Code, seen, reads.Load(), w.Header().Get("Content-Type"))
				}
			} else if w.Code != http.StatusForbidden || seen != -1 || reads.Load() != 0 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("status %d, registered %d, reads %d, type %q; want 403 before registration or streaming", w.Code, seen, reads.Load(), w.Header().Get("Content-Type"))
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

// A topic stream asks conversation for the topic with the resolved
// membership and answers its ErrTopicNotFound with 404 before anything is
// registered or sent. The stream is configured and the session valid, so
// only the lookup can refuse it; the found case proves the stream would start.
func testTopicStreamLookup(t *testing.T) {
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	for _, tt := range []struct {
		name      string
		lookupErr error
		status    int
	}{
		{"found", nil, http.StatusOK},
		{"not found", conversation.ErrTopicNotFound, http.StatusNotFound},
		{"lookup failure", errors.New("offline"), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			seen := -1
			var reads atomic.Int32
			var lookups [][3]kernel.ID
			p := channelPages{channels: &fakeChannels{}, topics: fakeTopics{err: tt.lookupErr, lookups: &lookups},
				stream: &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Sessions: laterSession{hub: hub, session: live, seen: &seen}}}
			membership := org.Membership{Organization: org.Organization{ID: kernel.ID{9}, Slug: "acme"}}
			handler := middleware.Session(oneSession{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.events(w, r, membership) }))
			selected := kernel.ID{3}
			r := httptest.NewRequest(http.MethodGet, streamEndpoint(t, view.ConversationURL("acme", kernel.ID{1}, &selected))+"after=0", nil)
			if !strings.Contains(t.Name(), "/organisation/") {
				r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
				r.SetPathValue("topicID", "03000000-0000-0000-0000-000000000000")
			}
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(w, r)
			if len(lookups) != 1 || lookups[0] != ([3]kernel.ID{{9}, {1}, {3}}) {
				t.Fatalf("topic lookups %v; want one, scoped to the membership's organisation", lookups)
			}
			streamed := w.Header().Get("Content-Type") == "text/event-stream; charset=utf-8"
			if w.Code != tt.status || streamed != (tt.status == http.StatusOK) {
				t.Fatalf("status %d, event stream %t; want %d", w.Code, streamed, tt.status)
			}
			if tt.status != http.StatusOK && (seen != -1 || reads.Load() != 0 || strings.Contains(w.Body.String(), "event:") || strings.Contains(w.Body.String(), "data:")) {
				t.Fatalf("registered %d, reads %d, body %q; want nothing before the refusal", seen, reads.Load(), w.Body.String())
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

func TestOpenStreamCleanup(t *testing.T) {
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}
	for _, tt := range []struct {
		name       string
		cookie     bool
		resolveErr error
		wantStatus int
	}{
		{name: "opened", cookie: true},
		{name: "missing cookie", wantStatus: http.StatusNotFound},
		{name: "session lookup failed", cookie: true, resolveErr: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			seen := -1
			p := channelPages{stream: &Streaming{Lifetime: t.Context(), Hub: hub, Sessions: laterSession{hub: hub, session: live, err: tt.resolveErr, seen: &seen}}}
			r := httptest.NewRequest(http.MethodGet, "/events", nil)
			if tt.cookie {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			}
			w := httptest.NewRecorder()
			ctx, cleanup, ok := p.openStream(w, r, kernel.ID{9}, kernel.ID{1}, live)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if tt.wantStatus != 0 {
				if ok || ctx != nil || cleanup != nil || w.Code != tt.wantStatus {
					t.Fatalf("openStream: ok=%t, context=%v, cleanup present=%t, status=%d; want failure with status %d", ok, ctx, cleanup != nil, w.Code, tt.wantStatus)
				}
			} else {
				if !ok || ctx == nil || cleanup == nil {
					t.Fatal("openStream did not return a context and cleanup")
				}
				if deadline, set := ctx.Deadline(); !set || !deadline.Equal(live.ExpiresAt) {
					t.Fatalf("deadline = %v, set=%t; want session expiry %v", deadline, set, live.ExpiresAt)
				}
				if ctx.Err() != nil || hub.Connections() != 1 {
					t.Fatal("successful open did not keep the stream alive and registered")
				}
				cleanup()
				// Cancelling the expiry context first preserves the old defer
				// order; unregister alone would leave ErrUnregistered as cause.
				if !errors.Is(context.Cause(ctx), context.Canceled) {
					t.Fatalf("cleanup cause = %v, want context.Canceled", context.Cause(ctx))
				}
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

// An account at its cap is refused with 429 before anything is streamed;
// once a slot is freed, the next stream starts.
func testStreamCapPerAccount(t *testing.T) {
	testStreamCap(t, realtime.NewHub(), kernel.ID{1})
}

func testStreamCapPerProcess(t *testing.T) {
	testStreamCap(t, realtime.NewHubWithMaxStreams(1), kernel.ID{2})
}

func testStreamCap(t *testing.T, hub *realtime.Hub, occupiedAccount kernel.ID) {
	t.Helper()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	live := identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	seen := -1
	var reads atomic.Int32
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Sessions: laterSession{hub: hub, session: live, seen: &seen}, MaxPerAccount: 1}
	}))
	if err != nil {
		t.Fatal(err)
	}
	url := streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1})) + "after=0"
	// Occupy either Alice's account slot or the process slot with another
	// account and organisation, leaving Alice's account slot available.
	_, unregister, err := hub.Register(t.Context(), realtime.Connection{Organization: kernel.ID{8}, Account: occupiedAccount, Session: kernel.ID{0x52}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	w := serveForm(handler, http.MethodGet, url, "live", nil)
	if seen != -1 || reads.Load() != 0 || hub.Connections() != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("refusal registered a stream, re-checked the session, read events or set SSE headers")
	}
	if w.Code != http.StatusTooManyRequests || strings.Contains(w.Body.String(), "data:") || w.Header().Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatalf("over the cap: status %d, type %q; want 429 before streaming", w.Code, w.Header().Get("Content-Type"))
	}
	unregister()
	// A writer that supports deadlines and flushing, so the freed slot
	// really starts a stream: the header goes out and the loop reads once.
	// The read then fails, which ends the stream.
	started := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	handler.ServeHTTP(started, r)
	if started.Code != http.StatusOK || started.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || seen != 1 || reads.Load() != 1 {
		t.Fatalf("after the slot was freed: status %d, type %q, %d registered at the re-check, %d reads; want a started stream", started.Code, started.Header().Get("Content-Type"), seen, reads.Load())
	}
	if n := hub.Connections(); n != 0 {
		t.Fatalf("%d connections registered after the stream ended, want 0", n)
	}
}

// Without a configured cap, DefaultMaxStreamsPerAccount applies.
func testStreamDefaultCap(t *testing.T) {
	hub := realtime.NewHub()
	for i := range DefaultMaxStreamsPerAccount {
		if _, _, err := hub.Register(t.Context(), realtime.Connection{Account: kernel.ID{1}, Session: kernel.ID{byte(i)}}, DefaultMaxStreamsPerAccount); err != nil {
			t.Fatal(err)
		}
	}
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	seen := -1
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: noEvents{}, Sessions: laterSession{hub: hub, seen: &seen}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if w := serveForm(handler, http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", "live", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d with %d streams open, want 429", w.Code, DefaultMaxStreamsPerAccount)
	}
}

// A stream that reaches registration after shutdown began gets 503, so the
// browser retries against the next process.
func testStreamRefusedDuringShutdown(t *testing.T) {
	hub := realtime.NewHub()
	hub.CancelAll()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	seen := -1
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: noEvents{}, Sessions: laterSession{hub: hub, seen: &seen}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := serveForm(handler, http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", "live", nil)
	if w.Code != http.StatusServiceUnavailable || seen != -1 {
		t.Fatalf("status %d (re-check ran: %t), want 503 before anything else", w.Code, seen != -1)
	}
}

type lifetimeReads struct {
	fakeMessages
	started  chan context.Context
	returned chan struct{}
	render   bool
}

func (r lifetimeReads) One(ctx context.Context, _ org.Membership, _ kernel.ID, _ int64) (conversation.Entry, error) {
	r.started <- ctx
	<-ctx.Done()
	r.returned <- struct{}{}
	return conversation.Entry{}, ctx.Err()
}

func (r lifetimeReads) EventsAfter(ctx context.Context, orgID kernel.ID, _ int64, _ int) ([]realtime.Event, error) {
	if r.render {
		return []realtime.Event{{OrganizationID: orgID, ChannelID: kernel.ID{1}, Seq: 1, Kind: conversation.KindPosted}}, nil
	}
	_, err := r.One(ctx, org.Membership{}, kernel.ID{}, 0)
	return nil, err
}

func testStreamCachesEndWithLifetime(t *testing.T) {
	for _, render := range []bool{false, true} {
		t.Run(fmt.Sprintf("render=%t", render), func(t *testing.T) {
			parent, stop := context.WithCancel(t.Context())
			defer stop()
			reads := lifetimeReads{started: make(chan context.Context, 1), returned: make(chan struct{}, 1), render: render}
			hub := realtime.NewHub()
			seen := 0
			catalogues, err := i18n.New(slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages = reads
				s.Stream = &Streaming{Lifetime: parent, Hub: hub, Events: realtime.NewCachedEvents(parent, reads, hub, 8, time.Minute),
					Sessions: laterSession{hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: &seen}}
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, streamEndpoint(t, view.ChannelURL("acme", kernel.ID{1}))+"after=0", nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			done := make(chan struct{}, 1)
			go func() {
				handler.ServeHTTP(&deadlineWriter{ResponseRecorder: httptest.NewRecorder()}, r)
				done <- struct{}{}
			}()
			select {
			case ctx := <-reads.started:
				stop()
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("load context survived shutdown")
				}
			case <-time.After(time.Second):
				t.Fatal("load never started")
			}
			for _, ended := range []chan struct{}{reads.returned, done} {
				select {
				case <-ended:
				case <-time.After(time.Second):
					t.Fatal("loader or handler survived shutdown")
				}
			}
			if r.Context().Err() != nil {
				t.Fatal("request cancellation masked lifetime cancellation")
			}
		})
	}
}

// Both transports run the same endpoint suites; the subtest selects only the URL.
func TestStreamEndpoints(t *testing.T) {
	for _, endpoint := range []string{"per-page", "organisation"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, suite := range []struct {
				name string
				run  func(*testing.T)
			}{
				{"StreamRechecksTheSessionAfterRegistering", testStreamRechecksTheSessionAfterRegistering},
				{"StreamCancelledBeforeTheFirstWrite", testStreamCancelledBeforeTheFirstWrite},
				{"StreamCancelledBeforeTheFirstWriteOverHTTP", testStreamCancelledBeforeTheFirstWriteOverHTTP},
				{"StreamFetchSite", testStreamFetchSite},
				{"TopicStreamLookup", testTopicStreamLookup},
				{"StreamCapPerAccount", testStreamCapPerAccount},
				{"StreamCapPerProcess", testStreamCapPerProcess},
				{"StreamDefaultCap", testStreamDefaultCap},
				{"StreamRefusedDuringShutdown", testStreamRefusedDuringShutdown},
				{"StreamCachesEndWithLifetime", testStreamCachesEndWithLifetime},
			} {
				t.Run(suite.name, suite.run)
			}
		})
	}
}

func streamEndpoint(t *testing.T, page string) string {
	t.Helper()
	if !strings.Contains(t.Name(), "/organisation/") {
		return page + "/events?"
	}
	parts := strings.Split(page, "/")
	endpoint := "/organizations/" + parts[2] + "/events?want=messages&channel=" + parts[4]
	if len(parts) > 5 {
		endpoint += "&topic=" + parts[6]
	}
	return endpoint + "&"
}
