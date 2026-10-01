package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
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

// deadlineWriter is a ResponseWriter whose deadline, write and flush can
// each fail; it records the calls http.ResponseController makes.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	calls                                []string
	setErr, clearErr, writeErr, flushErr error
}

func (w *deadlineWriter) SetWriteDeadline(d time.Time) error {
	if d.IsZero() {
		w.calls = append(w.calls, "clear")
		return w.clearErr
	}
	w.calls = append(w.calls, "set")
	return w.setErr
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.calls = append(w.calls, "write")
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(b)
}

func (w *deadlineWriter) FlushError() error {
	w.calls = append(w.calls, "flush")
	return w.flushErr
}

// noFlushWriter supports deadlines but cannot flush.
type noFlushWriter struct{ http.ResponseWriter }

func (noFlushWriter) SetWriteDeadline(time.Time) error { return nil }

func TestSSESender(t *testing.T) {
	failure := errors.New("broken pipe")
	out := realtime.Outgoing{ID: 7, Name: "message", Data: []byte("<li>\r\nline one\nline two</li>")}
	tests := []struct {
		name      string
		writer    *deadlineWriter
		wantCalls []string
	}{
		{"delivered", &deadlineWriter{}, []string{"set", "write", "flush", "clear"}},
		{"deadline cannot be set", &deadlineWriter{setErr: failure}, []string{"set"}},
		{"write fails", &deadlineWriter{writeErr: failure}, []string{"set", "write"}},
		{"flush fails", &deadlineWriter{flushErr: failure}, []string{"set", "write", "flush"}},
		{"deadline cannot be cleared", &deadlineWriter{clearErr: failure}, []string{"set", "write", "flush", "clear"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.writer.ResponseRecorder = httptest.NewRecorder()
			s := &sseSender{w: tt.writer, rc: http.NewResponseController(tt.writer), timeout: time.Second}
			err := s.Send(context.Background(), out)
			if (err != nil) != (tt.name != "delivered") || (err != nil && !errors.Is(err, failure)) {
				t.Fatalf("Send = %v", err)
			}
			if !slices.Equal(tt.writer.calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v: nothing may follow a failure", tt.writer.calls, tt.wantCalls)
			}
			if tt.name == "delivered" {
				want := "id: 7\nevent: message\ndata: <li>\ndata: line one\ndata: line two</li>\n\n"
				if got := tt.writer.Body.String(); got != want {
					t.Fatalf("framed %q, want %q", got, want)
				}
			}
		})
	}

	t.Run("heartbeat", func(t *testing.T) {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := s.Heartbeat(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := w.Body.String(); got != ": heartbeat\n\n" {
			t.Fatalf("heartbeat %q, want an SSE comment", got)
		}
		if want := []string{"set", "write", "flush", "clear"}; !slices.Equal(w.calls, want) {
			t.Fatalf("calls = %v, want %v: a heartbeat is written like an event", w.calls, want)
		}
	})

	t.Run("writer that cannot flush", func(t *testing.T) {
		w := noFlushWriter{httptest.NewRecorder()}
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := s.Send(context.Background(), out); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("Send = %v, want http.ErrNotSupported", err)
		}
	})
}

// countingMessages is a MessageReader whose One is counted, can block and
// can fail. The body names what was read, so tests can tell renders apart.
type countingMessages struct {
	fakeMessages
	calls   *atomic.Int32
	release chan struct{}
	err     error
}

func (c countingMessages) One(_ context.Context, m authz.Membership, channel domain.ID, seq int64) (message.Entry, error) {
	c.calls.Add(1)
	if c.release != nil {
		<-c.release
	}
	if c.err != nil {
		return message.Entry{}, c.err
	}
	body := fmt.Sprintf("org %v channel %v seq %d", m.Organization.ID, channel, seq)
	return message.Entry{Message: domain.Message{ID: domain.ID{7}, Body: body}, DisplayName: "Alice", Handle: "alice"}, nil
}

// waitForRenderWaiters blocks until n renders have joined key's load.
func waitForRenderWaiters(t *testing.T, renders *realtime.Cache[renderKey, realtime.Outgoing], key renderKey, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); renders.Waiting(key) < n; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d renders joined the load", renders.Waiting(key), n)
		}
	}
}

// Streams of one organisation share each rendered message; everything that
// changes the HTML (organisation, channel, sequence, language) is in the
// key; a failed read reaches every render waiting on it and is not kept.
func TestMessageRendererSharesRenders(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	inLanguage := func(lang string) context.Context {
		var ctx context.Context
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", lang)
		catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
		return ctx
	}
	en := inLanguage("en")
	orgA, orgB := domain.ID{1}, domain.ID{3}
	memberOf := func(org domain.ID) authz.Membership {
		return authz.Membership{Organization: domain.Organization{ID: org}}
	}
	event := domain.Event{OrganizationID: orgA, Seq: 9, Kind: domain.EventMessagePosted, ChannelID: domain.ID{2}}
	keyOf := func(org domain.ID, e domain.Event) renderKey {
		return renderKey{organization: org, channel: e.ChannelID, seq: e.Seq, language: i18n.Language(en)}
	}

	t.Run("concurrent renders read once", func(t *testing.T) {
		calls := &atomic.Int32{}
		release := make(chan struct{})
		r := messageRenderer{messages: countingMessages{calls: calls, release: release}, membership: memberOf(orgA), renders: newRenderCache()}
		const renders = 20
		var wg sync.WaitGroup
		for range renders {
			wg.Go(func() {
				out, err := r.Render(en, realtime.Subscription{}, event)
				if err != nil || out.ID != 9 || !strings.Contains(string(out.Data), "seq 9") {
					t.Errorf("Render = %+v, %v", out, err)
				}
			})
		}
		waitForRenderWaiters(t, r.renders, keyOf(orgA, event), renders)
		close(release)
		wg.Wait()
		if n := calls.Load(); n != 1 {
			t.Fatalf("%d message reads for %d concurrent renders, want 1", n, renders)
		}
	})

	t.Run("each part of the key is its own entry", func(t *testing.T) {
		calls := &atomic.Int32{}
		shared := newRenderCache()
		render := func(ctx context.Context, org domain.ID, e domain.Event) string {
			t.Helper()
			e.OrganizationID = org
			r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(org), renders: shared}
			out, err := r.Render(ctx, realtime.Subscription{}, e)
			if err != nil {
				t.Fatal(err)
			}
			return string(out.Data)
		}
		otherChannel, otherSeq := event, event
		otherChannel.ChannelID = domain.ID{4}
		otherSeq.Seq = 10
		cases := []struct {
			name string
			ctx  context.Context
			org  domain.ID
			e    domain.Event
		}{
			{"base", en, orgA, event},
			{"organisation", en, orgB, event},
			{"channel", en, orgA, otherChannel},
			{"sequence", en, orgA, otherSeq},
			{"language", inLanguage("ja"), orgA, event},
		}
		seen := map[string]string{}
		for i, c := range cases {
			got := render(c.ctx, c.org, c.e)
			if n := calls.Load(); int(n) != i+1 {
				t.Fatalf("%s: %d reads after %d renders, want its own read", c.name, n, i+1)
			}
			// A message item may hold no translated text, so another
			// language needs only its own entry, not different HTML.
			if other, ok := seen[got]; ok && c.name != "language" {
				t.Fatalf("%s rendered the same HTML as %s", c.name, other)
			}
			seen[got] = c.name
		}
		if want := fmt.Sprintf("org %v", orgB); !strings.Contains(render(en, orgB, event), want) {
			t.Fatalf("organisation B was served another organisation's render")
		}
	})

	t.Run("a failure reaches every waiter and is not kept", func(t *testing.T) {
		calls := &atomic.Int32{}
		release := make(chan struct{})
		failure := errors.New("database unavailable")
		shared := newRenderCache()
		failing := messageRenderer{messages: countingMessages{calls: calls, release: release, err: failure}, membership: memberOf(orgA), renders: shared}
		const renders = 10
		var wg sync.WaitGroup
		for range renders {
			wg.Go(func() {
				if _, err := failing.Render(en, realtime.Subscription{}, event); !errors.Is(err, failure) {
					t.Errorf("Render = %v, want %v", err, failure)
				}
			})
		}
		waitForRenderWaiters(t, shared, keyOf(orgA, event), renders)
		close(release)
		wg.Wait()
		r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(orgA), renders: shared}
		if _, err := r.Render(en, realtime.Subscription{}, event); err != nil || calls.Load() != 2 {
			t.Fatalf("retry after a failure: %v after %d reads, want one fresh read", err, calls.Load())
		}
	})
}

// laterSession answers the stream's second look at the session. It records
// how many connections the hub held at that moment (the stream must already
// be registered), and can end the session right then, as a sign-out landing
// just after registration would.
type laterSession struct {
	hub       *realtime.Hub
	session   auth.Session
	err       error
	cancelNow bool
	seen      *int
}

func (l laterSession) Resolve(context.Context, string) (domain.Account, auth.Session, error) {
	*l.seen = l.hub.Connections()
	if l.cancelNow {
		l.hub.CancelSession(l.session.ID)
	}
	return domain.Account{ID: domain.ID{1}}, l.session, l.err
}

// noEvents is an event log that is never read in these tests.
type noEvents struct{}

func (noEvents) EventsAfter(context.Context, domain.ID, int64, int) ([]domain.Event, error) {
	return nil, errors.New("the stream must not start")
}

// The middleware resolves the session, then it is deleted before the stream
// registers: nothing would cancel the stream, so the second look must stop it
// before anything is sent, and the registration must not outlive it.
func TestStreamRechecksTheSessionAfterRegistering(t *testing.T) {
	live := auth.Session{ID: domain.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	for _, tt := range []struct {
		name  string
		later laterSession
	}{
		{"session deleted", laterSession{err: auth.ErrNoSession}},
		{"session replaced by another", laterSession{session: auth.Session{ID: domain.ID{0x77}, ExpiresAt: time.Now().Add(time.Hour)}}},
		// The session is still valid at the second look but ends at that
		// instant: only a registration made before it can be cancelled.
		{"signed out right after registering", laterSession{session: live, cancelNow: true}},
		// The sign-out lands while the second look is still querying: the
		// store wraps the cancelled context as an error, which must end the
		// stream like a deleted session, not as a server error.
		{"signed out during the second look", laterSession{session: live, cancelNow: true, err: fmt.Errorf("resolving session: %w", context.Canceled)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := realtime.NewHub()
			seen := -1
			tt.later.hub, tt.later.seen = hub, &seen
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Stream = &Streaming{Hub: hub, Events: noEvents{}, Authorizer: nil, Sessions: tt.later}
			}))
			if err != nil {
				t.Fatal(err)
			}
			w := serveForm(handler, http.MethodGet, view.ChannelURL("acme", domain.ID{1})+"/events?after=0", "live", nil)
			if seen != 1 {
				t.Fatalf("the second look saw %d registered connections, want 1: register before re-checking", seen)
			}
			if strings.Contains(w.Body.String(), "event:") || strings.Contains(w.Body.String(), "data:") {
				t.Fatalf("status %d, body %q; want nothing streamed", w.Code, w.Body.String())
			}
			if (!tt.later.cancelNow || tt.later.err != nil) && w.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404", w.Code)
			}
			if n := hub.Connections(); n != 0 {
				t.Fatalf("%d connections still registered", n)
			}
		})
	}
}

// blockingWriter's Write blocks until a deadline at or before now is set,
// like a connection whose client stopped reading.
type blockingWriter struct {
	*httptest.ResponseRecorder
	started, unblocked chan struct{}
}

func (w *blockingWriter) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() && !d.After(time.Now()) {
		select {
		case <-w.unblocked:
		default:
			close(w.unblocked)
		}
	}
	return nil
}

func (w *blockingWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.unblocked
	return 0, errors.New("i/o timeout")
}

func (*blockingWriter) FlushError() error { return nil }

func TestSSESenderStopsWhenCancelled(t *testing.T) {
	cause := errors.New("session ended")
	out := realtime.Outgoing{ID: 1, Name: "message", Data: []byte("x")}

	t.Run("before sending", func(t *testing.T) {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := s.Send(ctx, out); !errors.Is(err, cause) || len(w.calls) != 0 {
			t.Fatalf("Send = %v after %v; want %v and no calls", err, w.calls, cause)
		}
	})

	t.Run("during a blocked write", func(t *testing.T) {
		w := &blockingWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), unblocked: make(chan struct{})}
		ctx, cancel := context.WithCancelCause(context.Background())
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Hour}
		done := make(chan error, 1)
		go func() { done <- s.Send(ctx, out) }()
		<-w.started
		cancel(cause)
		select {
		case err := <-done:
			if !errors.Is(err, cause) {
				t.Fatalf("Send = %v, want %v", err, cause)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a cancelled stream stayed blocked in its write")
		}
	})
}

// gatedWriter lets a test hold the cancellation callback's SetWriteDeadline
// (a deadline at or before now) while the write and flush complete.
type gatedWriter struct {
	*httptest.ResponseRecorder
	flushing, cancelled, gate, interrupted chan struct{}
}

func (w *gatedWriter) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() && !d.After(time.Now()) {
		<-w.gate
		close(w.interrupted)
	}
	return nil
}

func (w *gatedWriter) FlushError() error {
	close(w.flushing)
	<-w.cancelled // the stream is cancelled while the flush is in progress
	return nil
}

// The write finishes while the cancellation callback is still running: Send
// must not return (and so the handler must not return) before the callback
// has finished touching the response.
func TestSSESenderWaitsForItsCancellationCallback(t *testing.T) {
	w := &gatedWriter{ResponseRecorder: httptest.NewRecorder(), flushing: make(chan struct{}), cancelled: make(chan struct{}), gate: make(chan struct{}), interrupted: make(chan struct{})}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("session ended")
	s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Hour}
	done := make(chan error, 1)
	go func() { done <- s.Send(ctx, realtime.Outgoing{ID: 1, Name: "message", Data: []byte("x")}) }()
	<-w.flushing
	cancel(cause) // starts the callback, which blocks at the gate
	close(w.cancelled)
	select {
	case err := <-done:
		t.Fatalf("Send returned %v while its cancellation callback was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(w.gate)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("Send = %v, want %v", err, cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never returned")
	}
	select {
	case <-w.interrupted:
	default:
		t.Fatal("Send returned before the callback finished")
	}
}

// stalledWriter is a connection whose client stopped reading: a write
// blocks until the write deadline passes, then fails as net/http would.
type stalledWriter struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	deadline time.Time
}

func (w *stalledWriter) SetWriteDeadline(d time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = d
	return nil
}

func (w *stalledWriter) Write([]byte) (int, error) {
	for {
		w.mu.Lock()
		d := w.deadline
		w.mu.Unlock()
		if !d.IsZero() && !time.Now().Before(d) {
			return 0, os.ErrDeadlineExceeded
		}
		time.Sleep(time.Millisecond)
	}
}

func (*stalledWriter) FlushError() error { return nil }

// A heartbeat to a client that stopped reading fails at the write deadline,
// which ends the stream.
func TestSSESenderHeartbeatFindsAStalledClient(t *testing.T) {
	w := &stalledWriter{ResponseRecorder: httptest.NewRecorder()}
	s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: 50 * time.Millisecond}
	started := time.Now()
	err := s.Heartbeat(context.Background())
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Heartbeat = %v, want the write deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Heartbeat took %v, want about the write timeout", elapsed)
	}
}

// An account at its cap is refused with 429 before anything is streamed;
// once a slot is freed, the next stream starts.
func TestStreamCapPerAccount(t *testing.T) {
	hub := realtime.NewHub()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	live := auth.Session{ID: domain.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)} // oneSession's
	seen := -1
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Hub: hub, Events: noEvents{}, Sessions: laterSession{hub: hub, session: live, seen: &seen}, MaxPerAccount: 1}
	}))
	if err != nil {
		t.Fatal(err)
	}
	url := view.ChannelURL("acme", domain.ID{1}) + "/events?after=0"
	// Alice (oneSession's account 1) already holds her one stream.
	_, unregister, err := hub.Register(t.Context(), realtime.Connection{Organization: domain.ID{9}, Account: domain.ID{1}, Session: domain.ID{0x52}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	w := serveForm(handler, http.MethodGet, url, "live", nil)
	if w.Code != http.StatusTooManyRequests || strings.Contains(w.Body.String(), "data:") || w.Header().Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatalf("over the cap: status %d, type %q; want 429 before streaming", w.Code, w.Header().Get("Content-Type"))
	}
	unregister()
	w = serveForm(handler, http.MethodGet, url, "live", nil)
	if w.Code != http.StatusOK || seen != 1 {
		t.Fatalf("after the slot was freed: status %d, %d registered at the re-check; want 200 and 1", w.Code, seen)
	}
	if n := hub.Connections(); n != 0 {
		t.Fatalf("%d connections registered after the stream ended, want 0", n)
	}
}

// Without a configured cap, DefaultMaxStreamsPerAccount applies.
func TestStreamDefaultCap(t *testing.T) {
	hub := realtime.NewHub()
	for i := range DefaultMaxStreamsPerAccount {
		if _, _, err := hub.Register(t.Context(), realtime.Connection{Account: domain.ID{1}, Session: domain.ID{byte(i)}}, DefaultMaxStreamsPerAccount); err != nil {
			t.Fatal(err)
		}
	}
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	seen := -1
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Stream = &Streaming{Hub: hub, Events: noEvents{}, Sessions: laterSession{hub: hub, seen: &seen}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if w := serveForm(handler, http.MethodGet, view.ChannelURL("acme", domain.ID{1})+"/events?after=0", "live", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d with %d streams open, want 429", w.Code, DefaultMaxStreamsPerAccount)
	}
}
