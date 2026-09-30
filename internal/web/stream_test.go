package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
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

	t.Run("writer that cannot flush", func(t *testing.T) {
		w := noFlushWriter{httptest.NewRecorder()}
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := s.Send(context.Background(), out); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("Send = %v, want http.ErrNotSupported", err)
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
			if tt.later.cancelNow == false && w.Code != http.StatusNotFound {
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
