package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func assertSidebarMatchesPage(t *testing.T, page *html.Node, frame realtime.Outgoing) {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(frame.Data))
	if err != nil || frame.Name != "sidebar" || !frame.Ephemeral || frame.ID != 0 {
		t.Fatalf("sidebar frame = %+v, %v", frame, err)
	}
	targets := 0
	for n := range doc.Descendants() {
		if attr(n, "hx-swap-oob") != "true" {
			continue
		}
		targets++
		id := attr(n, "id")
		for i, a := range n.Attr {
			if a.Key == "hx-swap-oob" {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
				break
			}
		}
		var got, want bytes.Buffer
		if err := html.Render(&got, n); err != nil {
			t.Fatal(err)
		}
		for target := range page.Descendants() {
			if attr(target, "id") == id {
				if err := html.Render(&want, target); err != nil {
					t.Fatal(err)
				}
			}
		}
		if got.String() != want.String() {
			t.Fatalf("%s recount differs from reload: %s; want %s", id, &got, &want)
		}
	}
	// The channel list carries the nested topic list (#872), so one target
	// replaces both, and the topics must arrive inside it.
	expected := 1
	nested := false
	for n := range doc.Descendants() {
		if attr(n, "id") == "sidebar-topics" {
			for p := n.Parent; p != nil; p = p.Parent {
				nested = nested || attr(p, "id") == "sidebar-channels"
			}
		}
	}
	if !nested {
		t.Fatal("sidebar frame lacks the topic list inside the channel list")
	}
	for n := range page.Descendants() {
		if attr(n, "id") == "topic-heading-count" {
			expected++
		}
	}
	if targets != expected {
		t.Fatalf("sidebar frame has %d targets, want %d", targets, expected)
	}
}

func TestSidebarRendererFreshLists(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	var ctx context.Context
	catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	m := org.Membership{Organization: org.Organization{Slug: "acme"}}
	snapshot := conversation.SidebarSnapshot{Current: conversation.Channel{ID: kernel.ID{1}, Name: "general"},
		ChannelCounts: map[kernel.ID]int64{}, TopicCounts: map[kernel.ID]int64{}}
	snapshot.Channels = []conversation.Channel{snapshot.Current}
	for i := range 50 {
		snapshot.Topics = append(snapshot.Topics, conversation.Topic{ID: kernel.ID{byte(i + 1)}, Name: fmt.Sprint(i)})
	}
	selected := snapshot.Topics[49]
	snapshot.Topic = &selected
	for i, count := range []int64{100, 0, 1, 9, 0, 1} {
		// Fresh lists displace the selection without extending the bound.
		if i == 3 {
			snapshot.Topics = append([]conversation.Topic{{ID: kernel.ID{99}, Name: "new"}}, snapshot.Topics[:49]...)
		}
		snapshot.ChannelCounts[snapshot.Current.ID], snapshot.TopicCounts[selected.ID] = count, count
		frame, err := RenderSidebar(ctx, m, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(bytes.NewReader(frame.Data))
		if err != nil {
			t.Fatal(err)
		}
		headingFound := false
		ids := []string{}
		for n := range doc.Descendants() {
			id := attr(n, "id")
			if strings.HasPrefix(id, "topic-") && id != "topic-heading-count" {
				ids = append(ids, id)
			}
			if id == view.ChannelDOMID(snapshot.Current.ID) || id == view.TopicDOMID(selected.ID) {
				// The entry's own link: the channel row also nests its topics.
				assertUnreadBadge(t, find(n, atom.A), "en", count)
			}
			if id == "topic-heading-count" {
				headingFound = true
				heading := int64(0)
				if i >= 3 {
					heading = count
				}
				assertUnreadBadge(t, n, "en", heading)
			}
		}
		if !headingFound {
			t.Fatal("recount omitted the stable heading target")
		}
		want := []string{}
		for _, topic := range snapshot.Topics {
			want = append(want, view.TopicDOMID(topic.ID))
		}
		if len(ids) != 50 || strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Fatalf("fresh topic list = %v, want %v", ids, want)
		}
	}
}
