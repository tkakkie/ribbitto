package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Streaming is what the channel event stream needs besides the message
// reader: the hub to wait on, the durable event log and the per-event
// authorization. A nil *Streaming turns the stream off (its route answers
// 404), which handler tests that do not exercise it rely on.
type Streaming struct {
	Hub        *realtime.Hub
	Events     realtime.EventReader
	Authorizer realtime.Authorizer
	// Sessions re-resolves the request's session after the stream has
	// registered (see events).
	Sessions middleware.SessionResolver
	// MaxPerAccount caps an account's open streams; zero means no cap until
	// #160 sets one.
	MaxPerAccount int
	// WriteTimeout bounds each write to the stream; zero means
	// DefaultStreamWriteTimeout. Tests shorten it.
	WriteTimeout time.Duration
}

// errSessionExpired ends a stream when its session expires.
var errSessionExpired = errors.New("session expired")

// DefaultStreamWriteTimeout bounds one event write. A client that cannot
// take a small event within it is not reading.
const DefaultStreamWriteTimeout = 10 * time.Second

// events serves GET …/channels/{channelID}/events: the channel's events after
// the client's cursor, as Server-Sent Events, until the client goes away or
// delivery fails. The organisation and account come from the URL and the
// session; the cursor is Last-Event-ID if the browser sends one (it does on
// its own reconnects), otherwise ?after.
func (p channelPages) events(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	if p.stream == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := p.service.Get(r.Context(), m, id)
	if errors.Is(err, channel.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "finding channel", err)
		return
	}
	after, ok := streamCursor(r)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	account, _ := middleware.Account(r.Context())
	session, ok := middleware.CurrentSession(r.Context())
	if !ok {
		http.NotFound(w, r)
		return
	}
	limit := p.stream.MaxPerAccount
	if limit <= 0 {
		limit = math.MaxInt
	}
	// Register first: from now on, deleting the session (sign-out, or a new
	// sign-in replacing it) cancels this stream through the hub.
	ctx, unregister, err := p.stream.Hub.Register(r.Context(), realtime.Connection{Organization: m.Organization.ID, Account: account.ID, Session: session.ID}, limit)
	if errors.Is(err, realtime.ErrTooManyConnections) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		serverError(w, r, "registering event stream", err)
		return
	}
	defer unregister()
	// Then look again: a session deleted between the middleware's lookup and
	// the registration cancelled nothing, so it must be caught here, before
	// anything is sent.
	cookie, err := r.Cookie(middleware.SessionCookie)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, again, err := p.stream.Sessions.Resolve(ctx, cookie.Value)
	if errors.Is(err, auth.ErrNoSession) || (err == nil && again.ID != session.ID) {
		http.NotFound(w, r)
		return
	}
	if err != nil && ctx.Err() != nil {
		// The session ended (or the client left) while it was being looked
		// up again: the store reports the cancellation as an error, but it
		// is the expected end of this stream, not a server failure.
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "re-checking the stream's session", err)
		return
	}
	// The session's expiry ends the stream too; nothing deletes an expired
	// session's row in time to cancel it.
	ctx, cancel := context.WithDeadlineCause(ctx, session.ExpiresAt, errSessionExpired)
	defer cancel()

	// The server's read timeout does not cut the stream: net/http clears the
	// read deadline when it starts the background read that watches for the
	// client going away (TestEventStream idles past it).
	rc := http.NewResponseController(w)
	timeout := p.stream.WriteTimeout
	if timeout <= 0 {
		timeout = DefaultStreamWriteTimeout
	}
	send := &sseSender{w: w, rc: rc, timeout: timeout}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	if err := send.write(ctx, func() error { w.WriteHeader(http.StatusOK); return nil }); err != nil {
		slog.WarnContext(r.Context(), "starting event stream", "err", err)
		return
	}

	stream := realtime.Stream{
		Hub: p.stream.Hub, Events: p.stream.Events, Authorizer: p.stream.Authorizer,
		Renderer: messageRenderer{messages: p.messages, membership: m},
	}
	sub := realtime.Subscription{Organization: m.Organization.ID, OrganizationSlug: m.Organization.Slug, Account: account.ID, Channel: c.ID}
	cursor, err := stream.Run(ctx, sub, after, send)
	if err != nil && ctx.Err() == nil {
		// Neither the client nor the session went away: delivery failed.
		// The browser reconnects with the last id it received, which is
		// cursor or less.
		slog.WarnContext(r.Context(), "event stream stopped", "cursor", cursor, "err", err)
	}
}

// streamCursor reads the cursor: Last-Event-ID, else the single ?after value.
// Both must be a non-negative sequence.
func streamCursor(r *http.Request) (int64, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		values := r.URL.Query()["after"]
		if len(values) != 1 {
			return 0, false
		}
		raw = values[0]
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	return cursor, err == nil && cursor >= 0
}

// messageRenderer renders a message event with the page's own component, so
// a live message is the same markup as one loaded with the page. It reads
// with the membership resolved when the stream opened; whether the member
// may still see the event is the Authorizer's decision, made just before.
type messageRenderer struct {
	messages   MessageReader
	membership authz.Membership
}

func (r messageRenderer) Render(ctx context.Context, _ realtime.Subscription, event domain.Event) (realtime.Outgoing, error) {
	entry, err := r.messages.One(ctx, r.membership, event.ChannelID, event.Seq)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	var html bytes.Buffer
	if err := view.MessageItem(viewMessage(entry)).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering message: %w", err)
	}
	return realtime.Outgoing{ID: event.Seq, Name: "message", Data: html.Bytes()}, nil
}

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
