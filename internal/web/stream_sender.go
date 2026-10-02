package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

// sseSender writes Server-Sent Events. Every write runs under its own finite
// deadline, replacing the server's WriteTimeout, which would otherwise cut
// the stream; after the flush the deadline is cleared, because an expired
// deadline cannot be extended and an idle stream must not carry one.
type sseSender struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

// write runs fn under a fresh deadline and flushes. Any failure, including
// a writer that cannot flush, ends the stream rather than buffering.
//
// A cancelled ctx (the session ended, or the client went away) stops it
// before anything is written, and interrupts a write or flush already
// blocked by moving the deadline to now, so a stream whose session ended
// does not keep a blocked write for the rest of the write timeout.
func (s *sseSender) write(ctx context.Context, fn func() error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	// The callback must never outlive this call: once the handler returns,
	// net/http recycles the response (HTTP/2 pools its state), and a late
	// SetWriteDeadline would touch it. So when stop reports that the
	// callback has been started, wait for it to finish.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		_ = s.rc.SetWriteDeadline(time.Now())
	})
	err := fn()
	if err == nil {
		err = s.rc.Flush()
		if err != nil {
			err = fmt.Errorf("flushing: %w", err)
		}
	}
	if !stop() {
		<-interrupted
		// Cancelled while writing: whatever the write returned, the stream
		// ends.
		return context.Cause(ctx)
	}
	if err != nil {
		return err
	}
	if err := s.rc.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clearing write deadline: %w", err)
	}
	return nil
}

// Heartbeat writes an SSE comment, which the browser ignores.
func (s *sseSender) Heartbeat(ctx context.Context) error {
	return s.write(ctx, func() error {
		_, err := s.w.Write([]byte(": heartbeat\n\n"))
		return err
	})
}

// Send writes one event. Each line of the data gets its own "data:" field;
// the browser joins them with newlines, so a multi-line body survives.
func (s *sseSender) Send(ctx context.Context, out realtime.Outgoing) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %d\nevent: %s\n", out.ID, out.Name)
	data := strings.ReplaceAll(strings.ReplaceAll(string(out.Data), "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return s.write(ctx, func() error {
		_, err := s.w.Write(b.Bytes())
		return err
	})
}
