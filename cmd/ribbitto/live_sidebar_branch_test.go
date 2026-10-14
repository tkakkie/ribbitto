package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

type sidebarBranchFixture struct {
	owner, reader   acceptanceBrowser
	streams         *httptest.Server
	channel, source kernel.ID
	path            string
	form            url.Values
}

func newSidebarBranchFixture(t *testing.T) sidebarBranchFixture {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "branch", "general")
	var source kernel.ID
	for i := 1; i <= 49; i++ {
		source = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
	}
	account := identitytest.Account(t, pool, "reader@example.org", "reader")
	member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "branch")
	acceptanceOK(t, err)
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, nil)
	read, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &source, "read moves")
	acceptanceOK(t, err)
	unreadPost, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &source, "unread moves")
	acceptanceOK(t, err)
	scope := unread.TopicScope{Scope: unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: member}, TopicID: source}
	acceptanceOK(t, unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), nil).Topic(t.Context(), scope, 1, read.EventSeq))
	var messages []string
	for _, id := range []kernel.ID{read.ID, unreadPost.ID} {
		messages = append(messages, fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]))
	}
	api, streams := streamServers(t, pool)
	path := view.ConversationURL("branch", f.Channel.ID, &source)
	return sidebarBranchFixture{sidebarBrowser(t, pool, api, f.AccountID), sidebarBrowser(t, pool, api, account), streams, f.Channel.ID, source, path,
		url.Values{"message": messages, "from": {path[strings.LastIndex(path, "/")+1:]}, "name": {"aaa"}}}
}

func sidebarBadge(t *testing.T, frame, id string, count int) {
	t.Helper()
	node := sidebarNode(t, frame, id)
	if count == 0 && id == "topic-heading-count" && strings.HasSuffix(node, `"></span>`) {
		return
	}
	want := fmt.Sprintf("Unread ribbits: %d", count)
	_, badge, _ := strings.Cut(node, `data-unread-count="`)
	doc, err := html.Parse(strings.NewReader(node))
	acceptanceOK(t, err)
	for n := range doc.Descendants() {
		var raw string
		var found, hidden bool
		for _, a := range n.Attr {
			if a.Key == "data-unread-count" {
				raw, found = a.Val, true
			}
			hidden = hidden || a.Key == "hidden"
		}
		if !found {
			continue
		}
		if raw != fmt.Sprint(count) || hidden != (count == 0) || (count > 0 && !strings.Contains(badge, want+"</span>")) {
			t.Fatalf("%s badge count=%q hidden=%t; want count %d hidden=%t: %s", id, raw, hidden, count, count == 0, node)
		}
		return
	}
	t.Fatalf("%s missing unread badge: %s", id, node)
}

func TestSidebarBadgeRendering(t *testing.T) {
	cat, err := i18n.New(slog.Default())
	acceptanceOK(t, err)
	channel := view.Channel{ID: kernel.ID{1}, Name: "general"}
	topic := view.Topic{ID: kernel.ID{2}, Name: "unlisted"}
	for _, count := range []int{0, 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			// An unlisted selection renders a real heading badge instead of an empty target.
			page := view.ChannelPage{Organization: view.Organization{Slug: "badges"}, Current: channel, Channels: []view.Channel{channel}, Topic: &topic,
				ChannelCounts: map[kernel.ID]int64{channel.ID: int64(count)}, TopicCounts: map[kernel.ID]int64{topic.ID: int64(count)}}
			var frame strings.Builder
			cat.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				acceptanceOK(t, view.SidebarLists(page).Render(r.Context(), &frame))
			})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(t.Context()))
			for _, target := range []struct{ name, id string }{{"channel", view.ChannelDOMID(channel.ID)}, {"heading", "topic-heading-count"}} {
				t.Run(target.name, func(t *testing.T) {
					if !strings.Contains(sidebarNode(t, frame.String(), target.id), `data-unread-count="`) {
						t.Fatalf("%s must contain a badge, not an empty target", target.id)
					}
					sidebarBadge(t, frame.String(), target.id, count)
				})
			}
		})
	}
}

func TestLiveSidebarBranchDisplacement(t *testing.T) {
	f := newSidebarBranchFixture(t)
	endpoint := strings.Replace(streamEndpoint(t, f.path), "want=messages", "want=sidebar", 1)
	var connections []<-chan sseEvent
	for i, b := range []acceptanceBrowser{f.reader, f.owner} {
		events, status, _ := openEndpointIn(t.Context(), t, on(b, f.streams), endpoint, "3", "")
		if status != 200 {
			t.Fatalf("connect status %d", status)
		}
		connections = append(connections, events)
		frame := sidebarReload(t, b, f.path, events)
		sidebarBadge(t, frame, view.TopicDOMID(f.source), 1-i)
		sidebarBadge(t, frame, view.ChannelDOMID(f.channel), 1-i)
		sidebarBadge(t, frame, "topic-heading-count", 0)
	}
	// The reader branches one read and one unread message. Only the other
	// member counts the notice; moving the originals preserves their read set.
	destination := branch(t, f.reader, view.ChannelURL("branch", f.channel), f.form, 303)
	destinationID := "topic-" + strings.ReplaceAll(destination[strings.LastIndex(destination, "/")+1:], "-", "")
	for i, b := range []acceptanceBrowser{f.reader, f.owner} {
		frame := sidebarReload(t, b, f.path, connections[i])
		list := sidebarNode(t, frame, "sidebar-topics")
		if strings.Count(list, "<li ") != 50 || strings.Contains(list, view.TopicDOMID(f.source)) || !strings.Contains(list, destinationID) {
			t.Fatal("branch did not replace the bounded list or left a displaced source badge")
		}
		sidebarBadge(t, frame, destinationID, 1-i)
		sidebarBadge(t, frame, "topic-heading-count", i)
		sidebarBadge(t, frame, view.ChannelDOMID(f.channel), 1)
	}
	post(t, f.owner, f.path, "unlisted source stays live")
	if frame := nextEvent(t, connections[0]); frame.name != "unread" {
		t.Fatalf("unlisted post = %+v", frame)
	}
	f.reader.visit(t, "POST", "/organizations/branch/sidebar/resync", nil, 204)
	frame := sidebarReload(t, f.reader, f.path, connections[0])
	sidebarBadge(t, frame, "topic-heading-count", 1)
	sidebarBadge(t, frame, view.ChannelDOMID(f.channel), 2)
}
