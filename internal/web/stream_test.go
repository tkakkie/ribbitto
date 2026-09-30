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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
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

// countingMessages is a MessageReader whose One is counted, can block and
// can fail.
type countingMessages struct {
	fakeMessages
	calls   *atomic.Int32
	release chan struct{}
	err     error
}

func (c countingMessages) One(context.Context, authz.Membership, domain.ID, int64) (message.Entry, error) {
	c.calls.Add(1)
	if c.release != nil {
		<-c.release
	}
	if c.err != nil {
		return message.Entry{}, c.err
	}
	return message.Entry{Message: domain.Message{ID: domain.ID{7}, Body: "shared"}, DisplayName: "Alice", Handle: "alice"}, nil
}

// Streams of one organisation share each rendered message: concurrent
// renders of one event read the message once; each language has its own
// entry; a failed read is not kept, and the next render retries.
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
	m := authz.Membership{Organization: domain.Organization{ID: domain.ID{1}}}
	event := domain.Event{OrganizationID: m.Organization.ID, Seq: 9, Kind: domain.EventMessagePosted, ChannelID: domain.ID{2}}
	calls := &atomic.Int32{}
	release := make(chan struct{})
	r := messageRenderer{messages: countingMessages{calls: calls, release: release}, membership: m, renders: newRenderCache()}

	var wg sync.WaitGroup
	en := inLanguage("en")
	for range 20 {
		wg.Go(func() {
			out, err := r.Render(en, realtime.Subscription{}, event)
			if err != nil || out.ID != 9 || !strings.Contains(string(out.Data), "shared") {
				t.Errorf("Render = %+v, %v", out, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d message reads for 20 concurrent renders, want 1", n)
	}
	if _, err := r.Render(inLanguage("ja"), realtime.Subscription{}, event); err != nil || calls.Load() != 2 {
		t.Fatalf("another language: %v after %d reads, want its own read", err, calls.Load())
	}

	failure := errors.New("database unavailable")
	failing := messageRenderer{messages: countingMessages{calls: calls, err: failure}, membership: m, renders: r.renders}
	other := event
	other.Seq = 10
	if _, err := failing.Render(en, realtime.Subscription{}, other); !errors.Is(err, failure) {
		t.Fatalf("Render = %v, want %v", err, failure)
	}
	before := calls.Load()
	if _, err := r.Render(en, realtime.Subscription{}, other); err != nil || calls.Load() != before+1 {
		t.Fatalf("retry after a failure: %v after %d reads, want a fresh read", err, calls.Load()-before)
	}
}
