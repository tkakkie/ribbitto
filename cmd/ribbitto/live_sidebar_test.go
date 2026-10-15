package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func sidebarBrowser(t *testing.T, pool *pgxpool.Pool, server *httptest.Server, account kernel.ID) acceptanceBrowser {
	t.Helper()
	b := newAcceptanceBrowser(t, server, "192.0.2.60")
	token, _, err := identitypg.NewSessions(pool, time.Now, nil).Create(t.Context(), account)
	acceptanceOK(t, err)
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: middleware.SessionCookie, Value: token, Path: "/", Secure: true}})
	return b
}

func sidebarNode(t *testing.T, body, id string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	acceptanceOK(t, err)
	for n := range doc.Descendants() {
		found := false
		for i := len(n.Attr) - 1; i >= 0; i-- {
			a := n.Attr[i]
			found = found || (a.Key == "id" && a.Val == id)
			if a.Key == "hx-swap-oob" {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			}
		}
		if found {
			var out bytes.Buffer
			acceptanceOK(t, html.Render(&out, n))
			return out.String()
		}
	}
	t.Fatalf("missing sidebar target %s", id)
	return ""
}

func sidebarReload(t *testing.T, b acceptanceBrowser, path string, events <-chan sseEvent) string {
	t.Helper()
	frame := readSidebarFrame(t, events)
	_, page := b.visit(t, "GET", path, nil, 200)
	ids := []string{"sidebar-channels", "sidebar-topics"}
	if strings.Contains(path, "/topics/") {
		ids = append(ids, "topic-heading-count")
	}
	for _, id := range ids {
		if sidebarNode(t, frame, id) != sidebarNode(t, page, id) {
			t.Fatalf("%s recount differs from reload", id)
		}
	}
	return frame
}

func TestLiveSidebar(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "live", "general")
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	selected := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "selected")
	account := identitytest.Account(t, pool, "reader@example.org", "reader")
	orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "live")
	acceptanceOK(t, err)
	gate := &sidebarGate{started: make(chan struct{}), release: make(chan struct{})}
	config := pool.Config()
	config.ConnConfig.Tracer = gate
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	acceptanceOK(t, err)
	defer traced.Close()
	unblock := sync.OnceFunc(func() { close(gate.release) })
	defer unblock()
	api, streams, hub := streamServersWithHub(t, traced)
	owner, reader := sidebarBrowser(t, pool, api, f.AccountID), sidebarBrowser(t, pool, api, account)
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, hub)
	for range 101 {
		_, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, nil, "cap")
		acceptanceOK(t, err)
	}
	path := view.ConversationURL("live", f.Channel.ID, &selected.ID)
	endpoint := strings.Replace(streamEndpoint(t, path), "want=messages", "want=sidebar", 1)
	var connections []<-chan sseEvent
	var closes []context.CancelFunc
	for _, b := range []acceptanceBrowser{owner, reader} {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		closes = append(closes, cancel)
		events, status, _ := openEndpointIn(ctx, t, on(b, streams), endpoint, "102", "")
		if status != 200 {
			t.Fatalf("connect status %d", status)
		}
		connections = append(connections, events)
		frame := sidebarReload(t, b, path, events)
		want := b.client == reader.client
		if strings.Contains(sidebarNode(t, frame, view.ChannelDOMID(f.Channel.ID)), "99+") != want ||
			sidebarHasUnread(t, frame, view.TopicDOMID(selected.ID)) {
			t.Fatal("member read state or capped/zero count incorrect")
		}
	}
	for _, channel := range []kernel.ID{other.ID, f.Channel.ID} {
		var topic *kernel.ID
		if channel == f.Channel.ID {
			topic = &selected.ID
		}
		_, err := posting.PostToTopic(t.Context(), m, channel, topic, "live")
		acceptanceOK(t, err)
		for i, b := range []acceptanceBrowser{owner, reader} {
			if i == 1 {
				frame := nextEvent(t, connections[i])
				if frame.name != "unread" || frame.id != "" {
					t.Fatalf("other member post delivery = %+v", frame)
				}
				b.visit(t, "POST", "/organizations/live/sidebar/resync", nil, 204)
			}
			frame := sidebarReload(t, b, path, connections[i])
			if sidebarHasUnread(t, frame, view.ChannelDOMID(other.ID)) != (i == 1) ||
				sidebarHasUnread(t, frame, view.TopicDOMID(selected.ID)) != (i == 1 && topic != nil) {
				t.Fatal("other-channel or selected-topic recount incorrect")
			}
		}
	}
	for i, cancel := range closes {
		cancel()
		streamEndsWithin(t, connections[i], time.Second)
	}
	// Only durable message delivery is requested alongside this recount.
	ctx, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	endpoint = strings.Replace(endpoint, "want=sidebar", "want=sidebar,messages", 1)
	events, _, _ := openEndpointIn(ctx, t, on(reader, streams), endpoint, "104", "")
	sidebarReload(t, reader, path, events)
	gate.armed.Store(true)
	post(t, owner, path, "disconnect here")
	message := nextEvent(t, events)
	if message.name != "message" || message.id != "105" {
		t.Fatalf("durable frame before disconnect = %+v", message)
	}
	reader.visit(t, "POST", "/organizations/live/sidebar/resync", nil, 204)
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("recount never started")
	}
	disconnect() // The durable frame arrived; its recount is still blocked.
	unblock()
	streamEndsWithin(t, events, 2*testServerWrite)
	events, _, _ = openEndpointIn(t.Context(), t, on(reader, streams), endpoint, "0", message.id)
	sidebarReload(t, reader, path, events)
	for _, page := range []string{view.ChannelURL("live", f.Channel.ID), path + "?before=105", view.ChannelURL("live", f.Channel.ID) + "/members"} {
		endpoint := strings.Replace(streamEndpoint(t, path), "want=messages", "want=sidebar", 1)
		if !strings.Contains(page, "/topics/") {
			endpoint = strings.Replace(streamEndpoint(t, view.ChannelURL("live", f.Channel.ID)), "want=messages", "want=sidebar", 1)
		}
		events, _, _ := openEndpointIn(t.Context(), t, on(reader, streams), endpoint, "105", "")
		sidebarReload(t, reader, page, events)
	}
}

// This gate is after membership resolution but inside the sidebar snapshot.
// Reconnect cancellation and revocation use the real delivery loop and authorizer.
type sidebarGate struct {
	armed            atomic.Bool
	started, release chan struct{}
	checking         chan struct{}
	checkOnce        sync.Once
}

func (g *sidebarGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if g.checking != nil && strings.Contains(data.SQL, "name: GetAccessEpoch") {
		g.checkOnce.Do(func() { close(g.checking) })
	}
	if strings.Contains(data.SQL, "name: CountTopicUnread") && g.armed.CompareAndSwap(true, false) {
		close(g.started)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*sidebarGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestLiveSidebarAccessLostDuringRecount(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "live", "general")
	gate := &sidebarGate{started: make(chan struct{}), release: make(chan struct{}), checking: make(chan struct{})}
	config := pool.Config()
	config.ConnConfig.Tracer = gate
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	acceptanceOK(t, err)
	defer traced.Close()
	unblock := sync.OnceFunc(func() { close(gate.release) })
	defer unblock()
	api, streams := streamServers(t, traced)
	b := sidebarBrowser(t, pool, api, f.AccountID)
	gate.armed.Store(true)
	endpoint := strings.Replace(streamEndpoint(t, view.ChannelURL("live", f.Channel.ID)), "want=messages", "want=sidebar", 1)
	events, status, _ := openEndpointIn(t.Context(), t, on(b, streams), endpoint, "1", "")
	if status != 200 {
		t.Fatalf("connect status %d", status)
	}
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("recount never started")
	}
	_, err = pool.Exec(t.Context(), "DELETE FROM member WHERE organization_id=$1 AND id=$2", f.OrganizationID, f.MemberID)
	acceptanceOK(t, err)
	unblock()
	select {
	case <-gate.checking:
	case <-time.After(5 * time.Second):
		t.Fatal("no fresh authorization check after recount")
	}
	noEvent(t, events, 2*testServerWrite)
}

func TestLiveSidebarTrigger(t *testing.T) {
	member, other, channel := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
	s := liveSidebar{membership: org.Membership{Member: org.Member{ID: member}}, channel: channel}
	for _, tc := range []struct {
		name  string
		event realtime.Event
		want  bool
	}{
		{"author", realtime.Event{Kind: conversation.KindPosted, Payload: conversation.EncodePosted(channel, other, other, member)}, true},
		{"other author", realtime.Event{Kind: conversation.KindPosted, Payload: conversation.EncodePosted(channel, other, other, other)}, false},
		{"legacy", realtime.Event{Kind: conversation.KindPosted, Payload: []byte(`{"channel_id":"03000000-0000-0000-0000-000000000000","message_id":"02000000-0000-0000-0000-000000000000"}`)}, false},
		{"malformed", realtime.Event{Kind: conversation.KindPosted, Payload: []byte(`{`)}, false},
		{"current move", realtime.Event{Kind: conversation.KindMessagesMoved, ChannelID: channel}, true},
		{"other move", realtime.Event{Kind: conversation.KindMessagesMoved, ChannelID: other}, false},
		{"join", realtime.Event{Kind: "member.joined"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Trigger(tc.event); got != tc.want {
				t.Fatalf("trigger = %t, want %t", got, tc.want)
			}
		})
	}
}

func sidebarHasUnread(t *testing.T, frame, id string) bool {
	t.Helper()
	_, badge, _ := strings.Cut(sidebarNode(t, frame, id), `data-unread-count="`)
	raw, _, _ := strings.Cut(badge, `"`)
	return raw != "" && raw != "0"
}
