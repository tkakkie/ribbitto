package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Short enough that a test outlasts them, so a stream that inherited any of
// them would be cut.
const (
	testStreamWrite = 150 * time.Millisecond
	testServerRead  = 300 * time.Millisecond
	testServerWrite = 300 * time.Millisecond
)

// streamServers serves one production handler, with the event stream on,
// from two servers sharing its hub: api with the production timeouts for
// setup, sign-up and posting (password hashing under the race detector can
// outlast a shortened timeout), and streams with every timeout shortened,
// where only the event streams are opened.
func streamServers(t *testing.T, pool *pgxpool.Pool) (api, streams *httptest.Server) {
	t.Helper()
	handler, _, err := buildHandler(pool, handlerConfig{setupToken: acceptanceToken, signupEnabled: true,
		trustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		hub:            realtime.NewHub(), streamWriteTimeout: testStreamWrite})
	acceptanceOK(t, err)
	start := func(timeouts serverTimeouts) *httptest.Server {
		server := httptest.NewUnstartedServer(handler)
		server.Config = newServer("", handler, timeouts)
		// HTTP/2, as production serves it through Caddy: an HTTP/2 stream
		// is reset as soon as its write deadline passes, even while idle,
		// which is why the stream clears its deadline after every write.
		server.EnableHTTP2 = true
		server.StartTLS()
		t.Cleanup(server.Close)
		return server
	}
	api = start(serverTimeouts{readHeader: readHeaderTimeout, read: readTimeout, idle: idleTimeout, write: writeTimeout})
	streams = start(serverTimeouts{readHeader: testServerRead, read: testServerRead, idle: time.Second, write: testServerWrite})
	return api, streams
}

// on returns the browser pointed at server, keeping its cookies: the jar
// matches the host, not the port.
func on(b acceptanceBrowser, server *httptest.Server) acceptanceBrowser {
	b.server = server
	return b
}

// sseEvent is one received event.
type sseEvent struct{ id, name, data string }

// openStream opens the channel's stream as the browser's session and returns
// its events as they arrive. The stream ends with the test.
func openStream(t *testing.T, b acceptanceBrowser, channelURL, after string) (<-chan sseEvent, int) {
	t.Helper()
	return openStreamWith(t, b, channelURL, after, "")
}

// openStreamWith also sends lastEventID as Last-Event-ID when it is not empty,
// as a browser does when it reconnects.
func openStreamWith(t *testing.T, b acceptanceBrowser, channelURL, after, lastEventID string) (<-chan sseEvent, int) {
	t.Helper()
	events, status, _ := openStreamProto(t, b, channelURL, after, lastEventID)
	return events, status
}

// openStreamProto also reports the HTTP major version the stream used.
func openStreamProto(t *testing.T, b acceptanceBrowser, channelURL, after, lastEventID string) (<-chan sseEvent, int, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, "GET", b.server.URL+channelURL+"/events?after="+after, nil)
	acceptanceOK(t, err)
	if lastEventID != "" {
		r.Header.Set("Last-Event-ID", lastEventID)
	}
	client := *b.client
	client.Timeout = 0 // the stream is long-lived; the context ends it
	response, err := client.Do(r)
	acceptanceOK(t, err)
	events := make(chan sseEvent, 16)
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		close(events)
		return events, response.StatusCode, response.ProtoMajor
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" || response.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("stream headers: %v", response.Header)
	}
	go func() {
		defer close(events)
		defer func() { _ = response.Body.Close() }()
		lines := bufio.NewReader(response.Body)
		var e sseEvent
		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				events <- e
				e = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				e.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				e.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if e.data != "" {
					e.data += "\n"
				}
				e.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return events, http.StatusOK, response.ProtoMajor
}

func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("stream ended")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return sseEvent{}
}

func noEvent(t *testing.T, events <-chan sseEvent, within time.Duration) {
	t.Helper()
	select {
	case e, ok := <-events:
		if ok {
			t.Fatalf("unexpected event %+v", e)
		}
	case <-time.After(within):
	}
}

// drain reads whatever the stream replays first, until it has been quiet
// for a moment.
func drain(t *testing.T, events <-chan sseEvent) {
	t.Helper()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				t.Fatal("stream ended while draining")
			}
		case <-time.After(300 * time.Millisecond):
			return
		}
	}
}

// streamEndsWithin waits for the server to end the stream, ignoring any events
// still in flight.
func streamEndsWithin(t *testing.T, events <-chan sseEvent, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("stream still open after %s", within)
		}
	}
}

// post sends the composer form without JavaScript (303 back to the channel).
func post(t *testing.T, b acceptanceBrowser, channelURL, body string) {
	t.Helper()
	response, err := b.client.Do(b.request(t, "POST", channelURL, url.Values{"body": {body}}))
	acceptanceOK(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	acceptanceOK(t, response.Body.Close())
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("post: status %d", response.StatusCode)
	}
}

func TestEventStream(t *testing.T) {
	pool := acceptanceDatabase(t)
	server, streams := streamServers(t, pool)
	owner := newAcceptanceBrowser(t, server, "192.0.2.10")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	channelURL := response.Header.Get("Location")
	member := newAcceptanceBrowser(t, server, "192.0.2.11")
	member.visit(t, "POST", "/signup", acceptanceForm("member"), 303)

	// Replay: two messages posted before the stream opens arrive in order,
	// each carrying its sequence as the id and the page's message markup.
	post(t, owner, channelURL, "first\nwith a second line")
	post(t, owner, channelURL, "second")
	events, status, proto := openStreamProto(t, on(owner, streams), channelURL, "0", "")
	if status != http.StatusOK || proto != 2 {
		t.Fatalf("owner's stream: status %d over HTTP/%d, want 200 over HTTP/2", status, proto)
	}
	first, second := nextEvent(t, events), nextEvent(t, events)
	if first.name != "message" || !strings.Contains(first.data, "first\nwith a second line") || !strings.Contains(first.data, `<li id="message-`) || second.id <= first.id || !strings.Contains(second.data, "second") {
		t.Fatalf("replayed %+v then %+v", first, second)
	}
	if !strings.Contains(first.data, "data-announcement=\"New message @owner: first\nwith a second line\"") ||
		!strings.Contains(second.data, `data-announcement="New message @owner: second"`) {
		t.Fatal("stream payloads must carry announcement text")
	}

	// Last-Event-ID wins over ?after: from the first event's id, replay starts
	// at the second even though ?after asks for everything. A malformed
	// header is refused rather than falling back to ?after.
	resumed, status := openStreamWith(t, on(owner, streams), channelURL, "0", first.id)
	if e := nextEvent(t, resumed); status != http.StatusOK || e.id != second.id {
		t.Fatalf("resumed from Last-Event-ID %s: status %d, first event %+v", first.id, status, e)
	}
	if _, status := openStreamWith(t, on(owner, streams), channelURL, "0", "x"); status != http.StatusBadRequest {
		t.Fatalf("malformed Last-Event-ID: %d, want 400", status)
	}

	// The member's stream, opened from the owner's last id, sees only what
	// follows; each event is readable before the next is posted.
	memberEvents, status := openStream(t, on(member, streams), channelURL, second.id)
	if status != http.StatusOK {
		t.Fatalf("member's stream: %d", status)
	}
	post(t, owner, channelURL, "live one")
	if e := nextEvent(t, memberEvents); !strings.Contains(e.data, "live one") {
		t.Fatalf("live: %+v", e)
	}
	nextEvent(t, events)

	// Idle for longer than every deadline the stream could have inherited:
	// the stream's own write deadline, and the server's read and write
	// timeouts. Delivery still works afterwards.
	time.Sleep(2 * testServerWrite)
	post(t, owner, channelURL, "after idle")
	if e := nextEvent(t, memberEvents); !strings.Contains(e.data, "after idle") {
		t.Fatalf("after idle: %+v", e)
	}
	nextEvent(t, events)

	// Sessions end their streams (#207). The owner signs in on two more
	// browsers; each has its own session and stream.
	laptop, phone := newAcceptanceBrowser(t, server, "192.0.2.14"), newAcceptanceBrowser(t, server, "192.0.2.15")
	for _, b := range []acceptanceBrowser{laptop, phone} {
		b.visit(t, "POST", "/signin", acceptanceForm("owner"), 303)
	}
	laptopEvents, _ := openStream(t, on(laptop, streams), channelURL, "0")
	phoneEvents, _ := openStream(t, on(phone, streams), channelURL, "0")
	drain(t, laptopEvents)
	drain(t, phoneEvents)
	// Signing out on the laptop ends the laptop's stream at once; the
	// phone's, another session of the same account, stays and still gets
	// the next post.
	laptop.visit(t, "POST", "/signout", nil, 303)
	streamEndsWithin(t, laptopEvents, 2*time.Second)
	post(t, owner, channelURL, "after the laptop signed out")
	if e := nextEvent(t, phoneEvents); !strings.Contains(e.data, "after the laptop signed out") {
		t.Fatalf("phone after the laptop's sign-out: %+v", e)
	}
	nextEvent(t, events)
	nextEvent(t, memberEvents)
	// Signing in again on the phone replaces its session: the old one's
	// stream ends.
	phone.visit(t, "POST", "/signin", acceptanceForm("owner"), 303)
	streamEndsWithin(t, phoneEvents, 2*time.Second)
	// A session that expires ends its stream when it expires.
	_, err := pool.Exec(t.Context(), "UPDATE session SET expires_at = now() + interval '1 second' WHERE id = (SELECT id FROM session ORDER BY created_at DESC LIMIT 1)")
	acceptanceOK(t, err)
	expiring, status := openStream(t, on(phone, streams), channelURL, "0")
	if status != http.StatusOK {
		t.Fatalf("expiring session's stream: %d", status)
	}
	streamEndsWithin(t, expiring, 5*time.Second)

	// Membership removed while the stream is open: the next event is
	// withheld from that stream, while the owner's still gets it.
	_, err = pool.Exec(t.Context(), "DELETE FROM member WHERE handle = 'member'")
	acceptanceOK(t, err)
	post(t, owner, channelURL, "after removal")
	if e := nextEvent(t, events); !strings.Contains(e.data, "after removal") {
		t.Fatalf("owner after removal: %+v", e)
	}
	noEvent(t, memberEvents, 500*time.Millisecond)

	// Nobody who may not read the channel gets a stream: signed out, and
	// the removed member (the organisation is not found for them now).
	anonymous := newAcceptanceBrowser(t, server, "192.0.2.12")
	for _, b := range []acceptanceBrowser{anonymous, member} {
		if _, status := openStream(t, on(b, streams), channelURL, "0"); status != http.StatusNotFound {
			t.Fatalf("stream for a non-member: %d, want 404", status)
		}
	}
	// Another organisation's member, with the stream on: 404 for this
	// organisation's channel, and 404 for this channel's id under their own
	// organisation's URL; their own channel streams, as a control.
	other := pgtest.OrganizationWithOwner(t, pool, "globex", "general")
	token, _, err := auth.NewSessions(postgres.NewSessionStore(pool), time.Now).Create(t.Context(), other.AccountID)
	acceptanceOK(t, err)
	outsider := newAcceptanceBrowser(t, server, "192.0.2.13")
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	outsider.client.Jar.SetCookies(u, []*http.Cookie{{Name: middleware.SessionCookie, Value: token, Path: "/", Secure: true}})
	ownChannel := view.ChannelURL("globex", other.Channel.ID)
	foreignID := strings.TrimPrefix(channelURL, "/organizations/owner/channels/")
	for path, want := range map[string]int{
		channelURL: http.StatusNotFound,
		"/organizations/globex/channels/" + foreignID: http.StatusNotFound,
		ownChannel: http.StatusOK,
	} {
		if _, status := openStream(t, on(outsider, streams), path, "0"); status != want {
			t.Fatalf("outsider's stream %s: %d, want %d", path, status, want)
		}
	}

	// A malformed cursor is refused before streaming.
	for _, after := range []string{"-1", "x"} {
		if _, status := openStream(t, on(owner, streams), channelURL, after); status != http.StatusBadRequest {
			t.Fatalf("after=%s: %d, want 400", after, status)
		}
	}
}

// Over HTTP/1.1 too, signing out ends the session's open stream at once:
// the interrupted write and its deadline belong to the connection there.
func TestEventStreamEndsOnSignOutOverHTTP1(t *testing.T) {
	pool := acceptanceDatabase(t)
	server, streams := streamServers(t, pool)
	owner := newAcceptanceBrowser(t, server, "192.0.2.20")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	channelURL := response.Header.Get("Location")

	http1 := newAcceptanceBrowser(t, server, "192.0.2.21")
	transport := http1.client.Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	http1.client.Transport = transport
	http1.visit(t, "POST", "/signin", acceptanceForm("owner"), 303)

	events, status, proto := openStreamProto(t, on(http1, streams), channelURL, "0", "")
	if status != http.StatusOK || proto != 1 {
		t.Fatalf("stream: status %d over HTTP/%d, want 200 over HTTP/1.1", status, proto)
	}
	post(t, owner, channelURL, "over http/1.1")
	if e := nextEvent(t, events); !strings.Contains(e.data, "over http/1.1") {
		t.Fatalf("event: %+v", e)
	}
	http1.visit(t, "POST", "/signout", nil, 303)
	streamEndsWithin(t, events, 2*time.Second)
}

// Shutdown ends open streams first, so it returns at once instead of
// waiting for them until its deadline.
func TestShutdownEndsOpenStreams(t *testing.T) {
	pool := acceptanceDatabase(t)
	hub := realtime.NewHub()
	handler, _, err := buildHandler(pool, handlerConfig{setupToken: acceptanceToken, signupEnabled: true,
		trustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, hub: hub})
	acceptanceOK(t, err)
	server := httptest.NewUnstartedServer(handler)
	server.Config = newServer("", handler, serverTimeouts{readHeader: readHeaderTimeout, read: readTimeout, idle: idleTimeout, write: writeTimeout})
	endStreamsOnShutdown(server.Config, hub)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	owner := newAcceptanceBrowser(t, server, "192.0.2.30")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	events, status := openStream(t, owner, response.Header.Get("Location"), "0")
	if status != http.StatusOK {
		t.Fatalf("stream: status %d", status)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := server.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with an open stream: %v after %v", err, time.Since(started))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v with an open stream, want it ended first", elapsed)
	}
	streamEndsWithin(t, events, 2*time.Second)
}
