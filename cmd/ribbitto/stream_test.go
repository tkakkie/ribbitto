package main

import (
	"bufio"
	"context"
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
		return events, response.StatusCode
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("stream over %s, want HTTP/2", response.Proto)
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
	return events, http.StatusOK
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
	events, status := openStream(t, on(owner, streams), channelURL, "0")
	if status != http.StatusOK {
		t.Fatalf("owner's stream: %d", status)
	}
	first, second := nextEvent(t, events), nextEvent(t, events)
	if first.name != "message" || !strings.Contains(first.data, "first\nwith a second line") || !strings.Contains(first.data, `<li id="message-`) || second.id <= first.id || !strings.Contains(second.data, "second") {
		t.Fatalf("replayed %+v then %+v", first, second)
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

	// Membership removed while the stream is open: the next event is
	// withheld from that stream, while the owner's still gets it.
	_, err := pool.Exec(t.Context(), "DELETE FROM member WHERE handle = 'member'")
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
