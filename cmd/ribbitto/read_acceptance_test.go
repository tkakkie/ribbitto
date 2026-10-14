package main

import (
	"context"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"github.com/tkakkie/ribbitto/internal/web"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func readTracedPool(t *testing.T, pool *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	c := pool.Config()
	c.ConnConfig.Tracer = tracer
	// Gated tests hold one connection per blocked read while streams keep
	// querying; pgxpool's default (the CPU count, at least 4) is too small
	// on CI's two-CPU runners.
	c.MaxConns = 16
	p, err := pgxpool.NewWithConfig(t.Context(), c)
	acceptanceOK(t, err)
	t.Cleanup(p.Close)
	return p
}

func readGateWait(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("read gate not reached")
	}
}

type readLockGate struct {
	sidebarGate
	locks   chan struct{}
	observe atomic.Bool
}

func (g *readLockGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if g.observe.Load() && strings.Contains(d.SQL, "name: LockChannelRead") {
		g.locks <- struct{}{}
	}
	// RangePredecessor runs only after the channel's FOR UPDATE succeeded.
	if strings.Contains(d.SQL, "name: RangePredecessor") && g.armed.CompareAndSwap(true, false) {
		close(g.started)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func TestReadSidebarConcurrentHTTP(t *testing.T) {
	pool := pgtest.New(t)
	g := &readLockGate{sidebarGate: sidebarGate{started: make(chan struct{}), release: make(chan struct{})}, locks: make(chan struct{}, 16)}
	unblock := sync.OnceFunc(func() { close(g.release) })
	defer unblock()
	// This persistence test deliberately holds five reads at the database lock.
	f := newReadSidebarFixture(t, readTracedPool(t, pool, g), middleware.NewMemberRateLimiterWithBudget(time.Now, 4, 16, 5))
	// Establish the lock row without reading any message.
	request := f.reader.request(t, "POST", f.feed+"/read", url.Values{"cursor": {"1"}})
	request.Header.Set("HX-Request", "true")
	response, body, err := f.reader.send(request)
	acceptanceOK(t, err)
	acceptanceResponse(t, response, body, 204)
	tabs := []<-chan sseEvent{readSidebarConnect(t, f.reader, f.streams, f.feed), readSidebarConnect(t, f.reader, f.streams, f.topicA)}
	for _, tab := range tabs {
		f.badges(t, tab, 2, 3, 5)
	}
	g.observe.Store(true)
	g.armed.Store(true)
	var workers sync.WaitGroup
	for i, cursor := range []string{"2", "6", "3", "6", "4"} {
		path := f.topicA
		if i%2 == 1 {
			path = f.feed
		}
		request := f.reader.request(t, "POST", path+"/read", url.Values{"cursor": {cursor}})
		request.Header.Set("HX-Request", "true")
		workers.Go(func() {
			response, body, err := f.reader.send(request)
			acceptanceOK(t, err)
			acceptanceResponse(t, response, body, 204)
		})
		readGateWait(t, g.locks)
		if i == 0 {
			readGateWait(t, g.started)
		}
	}
	unblock()
	workers.Wait()
	// Reordered older topic cursors cannot undo the newest feed coverage.
	var hi int64
	acceptanceOK(t, pool.QueryRow(t.Context(), "SELECT hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND lo=0", f.organization, f.channel, f.member).Scan(&hi))
	if hi != 7 {
		t.Fatalf("persisted final prefix = %d; want 7", hi)
	}
	for i, tab := range tabs {
		// Intermediate coalesced frames are allowed; exact reload equality is final.
		path := f.feed
		if i == 1 {
			path = f.topicA
		}
		for frame := readSidebarFrame(t, tab); ; frame = readSidebarFrame(t, tab) {
			_, reload := f.reader.visit(t, "GET", path, nil, 200)
			if sidebarNode(t, frame, "sidebar-topics") == sidebarNode(t, reload, "sidebar-topics") && sidebarNode(t, frame, "sidebar-channels") == sidebarNode(t, reload, "sidebar-channels") {
				sidebarBadge(t, frame, view.ChannelDOMID(f.channel), 0)
				break
			}
		}
	}
}

func TestReadSidebarInitialSnapshotHTTP(t *testing.T) {
	pool := pgtest.New(t)
	g := &sidebarGate{started: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(g.release) })
	defer unblock()
	f := newReadSidebarFixture(t, readTracedPool(t, pool, g))
	g.armed.Store(true)
	tab := readSidebarConnect(t, f.reader, f.streams, f.topicA)
	readGateWait(t, g.started)
	f.read(t, f.topicA)
	unblock()
	// The first snapshot predates commit; its captured generation must not
	// acknowledge the publication that arrived while it was rendering.
	f.badges(t, tab, 2, 3, 5)
	f.badges(t, tab, 0, 3, 3)
}

type readPublicationSpy struct {
	changes unread.Changes
	calls   atomic.Int32
	drop    atomic.Bool
}

func (p *readPublicationSpy) ReadChanged(org, member kernel.ID) {
	p.calls.Add(1)
	if !p.drop.Swap(false) {
		p.changes.ReadChanged(org, member)
	}
}

// Only fault injection uses this composition; other acceptance uses buildHandler.
// The real posting adapters share the spy's environment but never call Reading.
func readFaultServer(t *testing.T, pool *pgxpool.Pool, p *readPublicationSpy) *httptest.Server {
	t.Helper()
	hub := realtime.NewHub()
	sessions := identitypg.NewSessions(pool, time.Now, hub)
	auth := orgpg.NewAuthorizer(pool)
	cat, err := i18n.New(slog.Default())
	acceptanceOK(t, err)
	kinds, err := realtime.MergeKinds(conversationpg.EventKinds(), orgpg.EventKinds())
	acceptanceOK(t, err)
	handler, err := web.NewHandler("", cat, web.Services{
		Sessions: sessions, SignIn: identitypg.NewSignIn(pool, nil, sessions), Authz: auth, ReadLimits: middleware.NewReadRateLimiter(time.Now), CreateLimits: middleware.NewChannelCreationRateLimiter(time.Now),
		Channels: conversationpg.NewChannels(pool, conversation.DefaultMaxChannels), Topics: conversationpg.NewTopics(pool),
		Messages: conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn),
		Posting:  conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, hub),
		Reading:  unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), p),
		Stream: &web.Streaming{Lifetime: t.Context(), Hub: hub, Sessions: sessions, Authorizer: auth,
			Events:  realtimepg.NewReader(pool, orgpg.BoundsIn, kinds),
			Sidebar: sidebarSource(conversationpg.NewReader(pool, nil, nil, eventCursor, pageCountsIn), &p.changes)},
	})
	acceptanceOK(t, err)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

type readCancelGate struct {
	sidebarGate
	cancelled chan struct{}
}

func (g *readCancelGate) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	ctx = g.sidebarGate.TraceQueryStart(ctx, conn, d)
	if ctx.Err() != nil && strings.Contains(d.SQL, "name: CountTopicUnread") {
		close(g.cancelled)
	}
	return ctx
}

func TestReadSidebarRecoveryHTTP(t *testing.T) {
	pool := pgtest.New(t)
	g := &readCancelGate{sidebarGate: sidebarGate{started: make(chan struct{}), release: make(chan struct{})}, cancelled: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(g.release) })
	defer unblock()
	f := newReadSidebarFixture(t, pool)
	p := &readPublicationSpy{}
	f.streams = readFaultServer(t, readTracedPool(t, pool, g), p)
	f.reader = sidebarBrowser(t, pool, f.streams, f.account)
	endpoint := strings.Replace(streamEndpoint(t, f.feed), "want=messages", "want=sidebar", 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tab, _, _ := openEndpointIn(ctx, t, f.reader, endpoint, "6", "")
	f.badges(t, tab, 2, 3, 5)
	w := p.changes.Watch(f.organization, f.member)
	defer w.Release()
	p.drop.Store(true)
	f.read(t, f.topicA)
	acceptanceCount(t, pool, 1, "SELECT count(*) FROM organization WHERE id=$1 AND event_seq=6", f.organization)
	if gen, _ := w.Snapshot(); gen != 0 || p.calls.Load() != 1 {
		t.Fatalf("omitted publication: generation=%d calls=%d", gen, p.calls.Load())
	}
	cancel()
	streamEndsWithin(t, tab, time.Second)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	// Last-Event-ID wins over after=0; the read created no durable event.
	tab, _, _ = openEndpointIn(ctx, t, f.reader, endpoint, "0", "6")
	f.badges(t, tab, 0, 3, 3)
	g.armed.Store(true)
	f.read(t, f.feed)
	readGateWait(t, g.started)
	cancel()
	readGateWait(t, g.cancelled)
	unblock()
	select {
	case frame, ok := <-tab:
		if ok {
			t.Fatalf("cancelled recount delivered %+v", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled recount did not end")
	}
	tab, _, _ = openEndpointIn(t.Context(), t, f.reader, endpoint, "0", "6")
	f.badges(t, tab, 0, 0, 0)
}

func TestReadSidebarPostingHTTP(t *testing.T) {
	for _, kind := range []string{"feed", "topic", "history"} {
		t.Run(kind, func(t *testing.T) {
			pool := pgtest.New(t)
			f := newReadSidebarFixture(t, pool)
			p := &readPublicationSpy{}
			f.streams = readFaultServer(t, pool, p)
			f.reader = sidebarBrowser(t, pool, f.streams, f.account)
			tabs := []<-chan sseEvent{readSidebarConnect(t, f.reader, f.streams, f.feed), readSidebarConnect(t, f.reader, f.streams, f.topicA)}
			for _, tab := range tabs {
				f.badges(t, tab, 2, 3, 5)
			}
			path, form := f.feed, url.Values{"body": {"own post"}, "cursor": {"6"}}
			a, b, channel := 0, 0, 0
			if kind != "feed" {
				path, b, channel = f.topicA, 3, 3
			}
			if kind == "history" {
				form.Del("cursor")
				a, channel = 2, 5
			}
			response, _, err := f.reader.send(f.reader.request(t, "POST", path, form))
			acceptanceOK(t, err)
			if response.StatusCode != 303 || response.Header.Get("Location") != path {
				t.Fatalf("posting response = %+v; want redirect to %s", response, path)
			}
			for _, tab := range tabs {
				f.badges(t, tab, a, b, channel)
			}
			if p.calls.Load() != 0 {
				t.Fatal("posting sent an extra private read publication")
			}
			// Prove this is a connected spy, not an unused counter.
			f.read(t, f.feed)
			want := int32(1)
			if kind == "feed" {
				want = 0
			}
			if p.calls.Load() != want {
				t.Fatalf("reading spy calls = %d; want %d", p.calls.Load(), want)
			}
		})
	}
}
