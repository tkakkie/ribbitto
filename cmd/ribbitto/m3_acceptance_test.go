package main

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

var pageCursor = regexp.MustCompile(`data-event-cursor="(\d+)"`)

// M3's goal end to end (#163), through the production handler, real HTTP
// and PostgreSQL: a message reaches another member's open stream without a
// reload, nothing posted between the page and its stream or while
// disconnected is lost or repeated, and nothing reaches a connection that
// may not read it. TestEventStream covers the stream's own contract (replay,
// Last-Event-ID, session end, removal, 404s); this follows a member's page.
func TestM3Acceptance(t *testing.T) {
	pool := acceptanceDatabase(t)
	server, streams := streamServers(t, pool)
	owner := newAcceptanceBrowser(t, server, "192.0.2.40")
	owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	response, _ := owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	channelURL := response.Header.Get("Location")
	member := newAcceptanceBrowser(t, server, "192.0.2.41")
	member.visit(t, "POST", "/signup", acceptanceForm("member"), 303)
	post(t, owner, channelURL, "before the page")

	// The member loads the page; a message posted before the page's stream
	// connects arrives through it, once, and nothing older is replayed.
	_, page := member.visit(t, "GET", channelURL, nil, 200)
	match := pageCursor.FindStringSubmatch(page)
	if match == nil || !strings.Contains(page, "before the page") {
		t.Fatal("the latest page must show the history and carry its cursor")
	}
	post(t, owner, channelURL, "between the page and its stream")
	ctx, drop := context.WithCancel(t.Context())
	memberEvents, status, _ := openStreamIn(ctx, t, on(member, streams), channelURL, match[1], "")
	if status != http.StatusOK {
		t.Fatalf("member's stream: %d", status)
	}
	if e := nextEvent(t, memberEvents); !strings.Contains(e.data, "between the page and its stream") {
		t.Fatalf("first event after the page: %+v", e)
	}
	noEvent(t, memberEvents, 300*time.Millisecond)

	// Another member's post reaches the open stream without a reload.
	post(t, owner, channelURL, "live")
	seen := nextEvent(t, memberEvents)
	if !strings.Contains(seen.data, "live") {
		t.Fatalf("live: %+v", seen)
	}

	// Another organisation's member has their own channel's stream open;
	// this organisation's posts never reach it.
	other := pgtest.OrganizationWithOwner(t, pool, "globex", "general")
	token, _, err := identity.NewSessions(postgres.NewSessionStore(pool), time.Now).Create(t.Context(), other.AccountID)
	acceptanceOK(t, err)
	outsider := newAcceptanceBrowser(t, server, "192.0.2.42")
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	outsider.client.Jar.SetCookies(u, []*http.Cookie{{Name: middleware.SessionCookie, Value: token, Path: "/", Secure: true}})
	outsiderEvents, status := openStream(t, on(outsider, streams), view.ChannelURL("globex", other.Channel.ID), "0")
	if status != http.StatusOK {
		t.Fatalf("outsider's own stream: %d", status)
	}

	// The member's connection drops; two posts land meanwhile. The browser
	// reconnects with the last id it received and gets exactly those two,
	// in order, then live delivery again.
	drop()
	streamEndsWithin(t, memberEvents, 2*time.Second)
	post(t, owner, channelURL, "missed one")
	post(t, owner, channelURL, "missed two")
	resumed, status := openStreamWith(t, on(member, streams), channelURL, match[1], seen.id)
	if status != http.StatusOK {
		t.Fatalf("reconnect: %d", status)
	}
	first, second := nextEvent(t, resumed), nextEvent(t, resumed)
	if !strings.Contains(first.data, "missed one") || !strings.Contains(second.data, "missed two") {
		t.Fatalf("after reconnecting: %+v then %+v, want exactly the two missed", first, second)
	}
	noEvent(t, resumed, 300*time.Millisecond)
	post(t, owner, channelURL, "live again")
	if e := nextEvent(t, resumed); !strings.Contains(e.data, "live again") {
		t.Fatalf("live after reconnecting: %+v", e)
	}
	noEvent(t, outsiderEvents, 300*time.Millisecond)

	// Signed out: the member's stream ends, and a new one is refused.
	member.visit(t, "POST", "/signout", nil, 303)
	streamEndsWithin(t, resumed, 2*time.Second)
	if _, status := openStream(t, on(member, streams), channelURL, "0"); status != http.StatusNotFound {
		t.Fatalf("signed-out stream: %d, want 404", status)
	}
	// An account with no membership in this organisation gets no stream.
	if _, status := openStream(t, on(outsider, streams), channelURL, "0"); status != http.StatusNotFound {
		t.Fatalf("outsider's stream of this channel: %d, want 404", status)
	}
}
