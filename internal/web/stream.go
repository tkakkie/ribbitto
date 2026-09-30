package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
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
	// WriteTimeout bounds each write to the stream; zero means
	// DefaultStreamWriteTimeout. Tests shorten it.
	WriteTimeout time.Duration
}

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
	if err := send.write(func() error { w.WriteHeader(http.StatusOK); return nil }); err != nil {
		slog.WarnContext(r.Context(), "starting event stream", "err", err)
		return
	}

	stream := realtime.Stream{
		Hub: p.stream.Hub, Events: p.stream.Events, Authorizer: p.stream.Authorizer,
		Renderer: messageRenderer{messages: p.messages, membership: m, renders: p.renders},
	}
	sub := realtime.Subscription{Organization: m.Organization.ID, OrganizationSlug: m.Organization.Slug, Account: account.ID, Channel: c.ID}
	cursor, err := stream.Run(r.Context(), sub, after, send)
	if err != nil && r.Context().Err() == nil {
		// The client did not go away: delivery failed. The browser
		// reconnects with the last id it received, which is cursor or less.
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
//
// Renders are shared between an organisation's streams through renders
// (#227): the output depends only on the message and the language, so
// streams of different members share it, and each language has its own.
type messageRenderer struct {
	messages   MessageReader
	membership authz.Membership
	renders    *realtime.Cache[renderKey, realtime.Outgoing]
}

type renderKey struct {
	organization, channel domain.ID
	seq                   int64
	language              string
}

// Render caching bounds: names shown in a live message can be up to
// renderTTL old (the page always reads them fresh).
const (
	renderCapacity = 4096
	renderTTL      = time.Minute
)

func newRenderCache() *realtime.Cache[renderKey, realtime.Outgoing] {
	return realtime.NewCache[renderKey, realtime.Outgoing](renderCapacity, renderTTL, 10*time.Second, nil, time.Now)
}

func (r messageRenderer) Render(ctx context.Context, _ realtime.Subscription, event domain.Event) (realtime.Outgoing, error) {
	key := renderKey{organization: r.membership.Organization.ID, channel: event.ChannelID, seq: event.Seq, language: i18n.Language(ctx)}
	return r.renders.Get(ctx, key, func(loadCtx context.Context) (realtime.Outgoing, error) {
		entry, err := r.messages.One(loadCtx, r.membership, event.ChannelID, event.Seq)
		if err != nil {
			return realtime.Outgoing{}, err
		}
		var html bytes.Buffer
		// The load's context keeps the caller's values (the language) but
		// not its cancellation: templ stops on a cancelled context, and one
		// stream going away must not fail the render others wait for.
		if err := view.MessageItem(viewMessage(entry)).Render(loadCtx, &html); err != nil {
			return realtime.Outgoing{}, fmt.Errorf("rendering message: %w", err)
		}
		return realtime.Outgoing{ID: event.Seq, Name: "message", Data: html.Bytes()}, nil
	})
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
func (s *sseSender) write(fn func() error) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	if err := fn(); err != nil {
		return err
	}
	if err := s.rc.Flush(); err != nil {
		return fmt.Errorf("flushing: %w", err)
	}
	if err := s.rc.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clearing write deadline: %w", err)
	}
	return nil
}

// Send writes one event. Each line of the data gets its own "data:" field;
// the browser joins them with newlines, so a multi-line body survives.
func (s *sseSender) Send(_ context.Context, out realtime.Outgoing) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %d\nevent: %s\n", out.ID, out.Name)
	data := strings.ReplaceAll(strings.ReplaceAll(string(out.Data), "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return s.write(func() error {
		_, err := s.w.Write(b.Bytes())
		return err
	})
}
