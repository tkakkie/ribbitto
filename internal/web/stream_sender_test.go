package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

// deadlineWriter is a ResponseWriter whose deadline, write and flush can
// each fail; it records the calls http.ResponseController makes.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	// mu guards calls: sseSender.write's AfterFunc sets a deadline from
	// another goroutine when the stream is cancelled mid-write.
	mu                                   sync.Mutex
	calls                                []string
	setErr, clearErr, writeErr, flushErr error
}

func (w *deadlineWriter) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, call)
}

func (w *deadlineWriter) SetWriteDeadline(d time.Time) error {
	if d.IsZero() {
		w.record("clear")
		return w.clearErr
	}
	w.record("set")
	return w.setErr
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.record("write")
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(b)
}

func (w *deadlineWriter) FlushError() error {
	w.record("flush")
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

func TestSSEEphemeralFrameAndPresenceToken(t *testing.T) {
	sub, ok := streamScope(httptest.NewRequest("GET", "/?want=presence&presence-after=instance:7", nil))
	if !ok || sub.PresenceAfter != "instance:7" {
		t.Fatalf("subscription=%+v valid=%v", sub, ok)
	}
	for _, name := range []string{"presence", "typing"} {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		s := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := s.Send(t.Context(), realtime.Outgoing{Ephemeral: true, ID: 999, Name: name, Data: []byte("state")}); err != nil {
			t.Fatal(err)
		}
		if got, want := w.Body.String(), "event: "+name+"\ndata: state\n\n"; got != want {
			t.Fatalf("frame=%q want=%q", got, want)
		}
	}
}
