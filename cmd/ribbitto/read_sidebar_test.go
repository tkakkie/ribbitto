package main

import (
	"context"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type readSidebarFixture struct {
	reader        acceptanceBrowser
	streams       *httptest.Server
	organization  kernel.ID
	member        kernel.ID
	account       kernel.ID
	channel, a, b kernel.ID
	feed, topicA  string
}

func newReadSidebarFixture(t *testing.T, pool *pgxpool.Pool, limits ...*middleware.MemberRateLimiter) readSidebarFixture {
	t.Helper()
	f := conversationtest.OrganizationWithOwner(t, pool, "read", "general")
	a := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "A")
	b := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "B")
	account := identitytest.Account(t, pool, "reader@example.org", "reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, nil)
	// Interleave topics so a topic read must preserve holes in the read set.
	for _, topic := range []kernel.ID{a.ID, b.ID, a.ID, b.ID, b.ID} {
		_, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &topic, "unread")
		acceptanceOK(t, err)
	}
	api, streams, _ := streamServersWithHub(t, pool, limits...)
	return readSidebarFixture{sidebarBrowser(t, pool, api, account), streams, f.OrganizationID, member, account, f.Channel.ID, a.ID, b.ID,
		view.ChannelURL("read", f.Channel.ID), view.ConversationURL("read", f.Channel.ID, &a.ID)}
}

func readSidebarConnect(t *testing.T, b acceptanceBrowser, streams *httptest.Server, page string) <-chan sseEvent {
	t.Helper()
	path := strings.Split(page, "?")[0]
	members := strings.HasSuffix(path, "/members")
	path = strings.TrimSuffix(path, "/members")
	endpoint := strings.Replace(streamEndpoint(t, path), "want=messages", "want=sidebar", 1)
	if members {
		endpoint += "panel=members&"
	}
	events, status, _ := openEndpointIn(t.Context(), t, on(b, streams), endpoint, "6", "")
	if status != 200 {
		t.Fatalf("sidebar connect status = %d", status)
	}
	return events
}

func readSidebarFrame(t *testing.T, events <-chan sseEvent) string {
	t.Helper()
	e := nextEvent(t, events)
	for e.name == "unread" {
		if e.id != "" {
			t.Fatal("unread frame advanced cursor")
		}
		e = nextEvent(t, events)
	}
	if e.name != "sidebar" || e.id != "" {
		t.Fatalf("read recount frame = %+v; want sidebar without id", e)
	}
	return e.data
}

func (f readSidebarFixture) badges(t *testing.T, events <-chan sseEvent, a, b, channel int) {
	t.Helper()
	frame := readSidebarFrame(t, events)
	sidebarBadge(t, frame, view.TopicDOMID(f.a), a)
	sidebarBadge(t, frame, view.TopicDOMID(f.b), b)
	sidebarBadge(t, frame, view.ChannelDOMID(f.channel), channel)
}

func (f readSidebarFixture) read(t *testing.T, path string) {
	t.Helper()
	r := f.reader.request(t, "POST", path+"/read", url.Values{"cursor": {"6"}})
	r.Header.Set("HX-Request", "true")
	response, body, err := f.reader.send(r)
	acceptanceOK(t, err)
	acceptanceResponse(t, response, body, 204)
}

func TestReadSidebarHTTPAllTabs(t *testing.T) {
	pool := pgtest.New(t)
	f := newReadSidebarFixture(t, pool)
	pages := []string{f.feed, f.topicA, view.ConversationURL("read", f.channel, &f.b), f.topicA + "?before=6", f.feed + "/members"}
	var tabs []<-chan sseEvent
	for _, page := range pages {
		// GETs, including older history and members views, leave the counts intact.
		f.reader.visit(t, "GET", page, nil, 200)
		events := readSidebarConnect(t, f.reader, f.streams, page)
		f.badges(t, events, 2, 3, 5)
		tabs = append(tabs, events)
	}
	f.read(t, f.topicA)
	for _, events := range tabs {
		f.badges(t, events, 0, 3, 3)
	}
	f.read(t, f.feed)
	for _, events := range tabs {
		f.badges(t, events, 0, 0, 0)
	}
}

func TestReadSidebarHTTPWithoutHub(t *testing.T) {
	pool := pgtest.New(t)
	f := newReadSidebarFixture(t, pool)
	handler, _, err := buildHandler(t.Context(), pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels})
	acceptanceOK(t, err)
	api := httptest.NewTLSServer(handler)
	t.Cleanup(api.Close)
	f.reader = sidebarBrowser(t, pool, api, f.account)
	f.read(t, f.topicA)
	f.read(t, f.feed)
}

// Only this pool serves the HTTP handler, so the first count statement is
// one recount, without fixture SQL or reloads contaminating the totals.
type readSidebarCounts struct {
	sidebarGate
	mu     sync.Mutex
	counts map[[2]kernel.ID]int
}

func (c *readSidebarCounts) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "name: FirstChannelReadRanges") {
		key := [2]kernel.ID{kernel.ID(data.Args[1].(pgtype.UUID).Bytes), kernel.ID(data.Args[2].(pgtype.UUID).Bytes)}
		c.mu.Lock()
		c.counts[key]++
		c.mu.Unlock()
	}
	return c.sidebarGate.TraceQueryStart(ctx, conn, data)
}

func TestReadSidebarHTTPIsolationAndNoop(t *testing.T) {
	pool := pgtest.New(t)
	counter := &readSidebarCounts{sidebarGate: sidebarGate{started: make(chan struct{}), release: make(chan struct{})}, counts: make(map[[2]kernel.ID]int)}
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	acceptanceOK(t, err)
	t.Cleanup(traced.Close)
	unblock := sync.OnceFunc(func() { close(counter.release) })
	t.Cleanup(unblock)
	f := newReadSidebarFixture(t, traced)
	account := identitytest.Account(t, pool, "other@example.org", "other")
	member := orgtest.Member(t, pool, f.organization, account, org.RoleMember, "other", 1)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	// No messages are needed there; use its valid durable cursor at connect.
	_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq=6,event_log_boundary_seq=6 WHERE id=$1", foreign.OrganizationID)
	acceptanceOK(t, err)
	other := sidebarBrowser(t, pool, f.reader.server, account)
	outsider := sidebarBrowser(t, pool, f.reader.server, foreign.AccountID)
	actor := readSidebarConnect(t, f.reader, f.streams, f.feed)
	f.badges(t, actor, 2, 3, 5)
	unaffected := []<-chan sseEvent{readSidebarConnect(t, other, f.streams, f.feed),
		readSidebarConnect(t, other, f.streams, f.topicA),
		readSidebarConnect(t, outsider, f.streams, view.ChannelURL("foreign", foreign.Channel.ID)),
		readSidebarConnect(t, outsider, f.streams, view.ChannelURL("foreign", foreign.Channel.ID))}
	for _, events := range unaffected {
		readSidebarFrame(t, events)
	}
	counter.armed.Store(true)
	f.read(t, f.topicA)
	select {
	case <-counter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("read-induced recount never reached the query gate")
	}
	unblock()
	f.badges(t, actor, 0, 3, 3)
	f.read(t, f.feed)
	f.badges(t, actor, 0, 0, 0)
	// Both routes repeat their last cursor, after all effective recounts arrived.
	f.read(t, f.topicA)
	f.read(t, f.feed)
	// A heartbeat is positive progress from the same writer, past any pending
	// one-second recount deadline. The collector exposes comments as empty frames.
	// A quiet timeout alone would not prove isolation or no-op suppression.
	for i, events := range append(unaffected, actor) {
		deadline := time.After(25 * time.Second)
	progress:
		for {
			select {
			case e, ok := <-events:
				if !ok {
					t.Fatalf("tab %d ended before heartbeat", i)
				}
				if e == (sseEvent{}) {
					break progress
				}
				t.Errorf("tab %d received read-induced frame instead of heartbeat: %+v", i, e)
			case <-deadline:
				t.Fatalf("tab %d made no heartbeat progress", i)
			}
		}
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	for key, want := range map[[2]kernel.ID]int{{f.organization, f.member}: 3, {f.organization, member}: 2, {foreign.OrganizationID, foreign.MemberID}: 2} {
		if got := counter.counts[key]; got != want {
			t.Errorf("org/member %v recounts = %d; want %d (isolation/no-op)", key, got, want)
		}
	}
}
