package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/presence"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// Streaming is what the event streams need besides the message
// reader: the hub to wait on, the durable event log and the per-event
// authorization. A nil *Streaming turns the stream off (its route answers
// 404), which handler tests that do not exercise it rely on.
type Streaming struct {
	// Lifetime is the process context; shutdown cancels shared render loads.
	Lifetime   context.Context
	Hub        *realtime.Hub
	Events     realtime.EventReader
	Authorizer realtime.Authorizer
	// Presence supplies the members page's snapshot, shared with its owner.
	Presence *presence.State
	// Owners adapts feature state to current frames.
	Owners map[realtime.Interest]realtime.EphemeralOwner
	// StreamOpened counts every accepted stream, regardless of interests.
	// Its returned cleanup releases that member's count.
	StreamOpened func(organizationID, memberID kernel.ID) func()
	// Sessions re-resolves the request's session after the stream has
	// registered (see openStream).
	Sessions middleware.SessionResolver
	// MaxPerAccount caps an account's open streams; zero means
	// DefaultMaxStreamsPerAccount.
	MaxPerAccount int
	// WriteTimeout bounds each write to the stream; zero means
	// DefaultStreamWriteTimeout. Tests shorten it.
	WriteTimeout time.Duration
	// Heartbeat is how long an idle stream waits before a heartbeat; zero
	// means DefaultStreamHeartbeat. Tests shorten it.
	Heartbeat time.Duration
}

// errSessionExpired ends a stream when its session expires.
var errSessionExpired = errors.New("session expired")

// DefaultStreamWriteTimeout bounds one event write. A client that cannot
// take a small event within it is not reading.
const DefaultStreamWriteTimeout = 10 * time.Second

// DefaultMaxStreamsPerAccount caps one account's open streams: several tabs
// on several devices fit, a runaway client does not. A stream over it is
// refused with 429 before it starts, rather than closing the oldest, which
// would make that tab reconnect and close the next, forever.
const DefaultMaxStreamsPerAccount = 16

// DefaultStreamHeartbeat is how often an idle stream writes a comment, so
// proxies keep it open and a client that stopped reading is found out at
// the write deadline.
const DefaultStreamHeartbeat = 20 * time.Second

// events resolves visibility before opening the organisation stream.
func (p channelPages) events(w http.ResponseWriter, r *http.Request, m org.Membership) {
	if p.stream == nil {
		http.NotFound(w, r)
		return
	}
	// CrossOriginProtection exempts GET, but a foreign page must not hold
	// the account's stream slots. Older clients may omit Fetch Metadata.
	if sites := r.Header.Values("Sec-Fetch-Site"); len(sites) != 0 && (len(sites) != 1 || sites[0] != "same-origin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	sub, ok := streamScope(r)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if sub.Channel != (kernel.ID{}) {
		_, err := p.channels.Get(r.Context(), m, sub.Channel)
		if errors.Is(err, conversation.ErrChannelNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			serverError(w, r, "finding channel", err)
			return
		}
	}
	// Interest is no permission: resolve visibility before registering or sending.
	if sub.Topic != nil {
		_, err := p.topics.Get(r.Context(), m, sub.Channel, *sub.Topic)
		if errors.Is(err, conversation.ErrTopicNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			serverError(w, r, "finding topic", err)
			return
		}
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
	ctx, cleanup, ok := p.openStream(w, r, m.Organization.ID, account.ID, m.Member.ID, session)
	if !ok {
		return
	}
	defer cleanup()

	// The server's read timeout does not cut the stream: net/http clears the
	// read deadline when it starts the background read that watches for the
	// client going away (TestStreamEndpoints idles past it).
	rc := http.NewResponseController(w)
	timeout := p.stream.WriteTimeout
	if timeout <= 0 {
		timeout = DefaultStreamWriteTimeout
	}
	send := &sseSender{w: w, rc: rc, timeout: timeout}
	// The status is committed without sseSender.write: on cancellation its
	// AfterFunc expires the write deadline, and over HTTP/2 that resets the
	// stream, so a 404 or 503 written afterwards would never arrive. Until
	// WriteHeader nothing has been sent, so a cancellation that wins first
	// (after the session re-check, too) still gets its status. One after
	// WriteHeader ends a stream that has started (accepted on #315).
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	if context.Cause(ctx) != nil {
		streamCancelled(w, r, ctx) // http.Error replaces the content type
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := send.write(ctx, func() error { return nil }); err != nil {
		slog.WarnContext(r.Context(), "starting event stream", "err", err)
		return
	}

	heartbeat := p.stream.Heartbeat
	if heartbeat <= 0 {
		heartbeat = DefaultStreamHeartbeat
	}
	stream := realtime.Stream{
		Hub: p.stream.Hub, Events: p.stream.Events, Authorizer: p.stream.Authorizer,
		Owners:    p.stream.Owners,
		Renderer:  messageRenderer{messages: p.messages, membership: m, renders: p.renders},
		Heartbeat: heartbeat,
	}
	sub.Organization, sub.OrganizationSlug, sub.Account = m.Organization.ID, m.Organization.Slug, account.ID
	cursor, err := stream.Run(ctx, sub, after, send)
	if err != nil && ctx.Err() == nil {
		// Neither the client nor the session went away: delivery failed.
		// The browser reconnects with the last id it received, which is
		// cursor or less.
		slog.WarnContext(r.Context(), "event stream stopped", "cursor", cursor, "err", err)
	}
}

// openStream keeps the register-then-re-check order that closes the race
// with sign-out. On success the caller must
// defer cleanup; on failure it has written the response and freed the slot.
func (p channelPages) openStream(w http.ResponseWriter, r *http.Request, organizationID, accountID, memberID kernel.ID, session identity.Session) (ctx context.Context, cleanup func(), ok bool) {
	limit := p.stream.MaxPerAccount
	if limit <= 0 {
		limit = DefaultMaxStreamsPerAccount
	}
	// Register first: from now on, deleting the session (sign-out, or a new
	// sign-in replacing it) cancels this stream through the hub.
	ctx, unregister, err := p.stream.Hub.Register(r.Context(), realtime.Connection{Organization: organizationID, Account: accountID, Session: session.ID}, limit)
	if errors.Is(err, realtime.ErrTooManyConnections) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return nil, nil, false
	}
	if errors.Is(err, realtime.ErrShutdown) {
		// The browser retries, and reaches the next process.
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	if err != nil {
		serverError(w, r, "registering event stream", err)
		return nil, nil, false
	}
	defer func() {
		if !ok {
			unregister()
		}
	}()
	// Then look again: a session deleted between the middleware's lookup and
	// the registration cancelled nothing, so it must be caught here, before
	// anything is sent.
	cookie, err := r.Cookie(middleware.SessionCookie)
	if err != nil {
		http.NotFound(w, r)
		return nil, nil, false
	}
	_, again, err := p.stream.Sessions.Resolve(ctx, cookie.Value)
	if errors.Is(err, identity.ErrNoSession) || (err == nil && again.ID != session.ID) {
		http.NotFound(w, r)
		return nil, nil, false
	}
	if err != nil && ctx.Err() != nil {
		// The session ended (or the client left) while it was being looked
		// up again: the store reports the cancellation as an error, but it
		// is the expected end of this stream, not a server failure.
		streamCancelled(w, r, ctx)
		return nil, nil, false
	}
	if err != nil {
		serverError(w, r, "re-checking the stream's session", err)
		return nil, nil, false
	}
	// The session's expiry ends the stream too; nothing deletes an expired
	// session's row in time to cancel it.
	ctx, cancel := context.WithDeadlineCause(ctx, session.ExpiresAt, errSessionExpired)
	if ctx.Err() != nil {
		cancel()
		streamCancelled(w, r, ctx)
		return nil, nil, false
	}
	closed := func() {}
	if p.stream.StreamOpened != nil {
		closed = p.stream.StreamOpened(organizationID, memberID)
	}
	return ctx, func() {
		cancel()
		unregister()
		closed()
	}, true
}

func streamCancelled(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if errors.Is(context.Cause(ctx), realtime.ErrShutdown) {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	http.NotFound(w, r)
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

// streamScope parses the organisation endpoint's interests; inactive interests
// stay explicit so accepting them cannot enable durable message delivery.
func streamScope(r *http.Request) (realtime.Subscription, bool) {
	var sub realtime.Subscription
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return sub, false
	}
	for key, values := range query {
		switch key {
		case "after": // Last-Event-ID takes precedence, including over a broken after.
		case "want", "channel", "topic", "presence-after":
			if len(values) != 1 || values[0] == "" {
				return sub, false
			}
		default:
			return sub, false
		}
	}
	for _, want := range strings.Split(query.Get("want"), ",") {
		interest := realtime.Interest(want)
		switch want {
		case "sidebar", "messages", "typing", "presence":
		default:
			return sub, false
		}
		if slices.Contains(sub.Interests, interest) {
			return sub, false
		}
		sub.Interests = append(sub.Interests, interest)
	}
	scoped := slices.Contains(sub.Interests, realtime.InterestMessages) || slices.Contains(sub.Interests, realtime.InterestTyping)
	raw := query.Get("channel")
	if scoped && raw == "" {
		return sub, false
	}
	if raw != "" {
		var ok bool
		sub.Channel, ok = pathID(raw)
		if !ok || sub.Channel == (kernel.ID{}) {
			return sub, false
		}
	}
	if raw := query.Get("topic"); raw != "" {
		selected, ok := pathID(raw)
		if !scoped || !ok {
			return sub, false
		}
		sub.Topic = &selected
	}
	if slices.Contains(sub.Interests, realtime.InterestPresence) && query.Get("presence-after") == "" {
		return sub, false
	}
	sub.PresenceAfter = query.Get("presence-after")
	return sub, true
}
