package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// A ripple's meaning comes from its look, not an added word (decision 28),
// so the branch icon must stay on every place that names a topic.
func TestTopicPresentation(t *testing.T) {
	catalogues, err := i18n.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = listedTopic{} }))
	if err != nil {
		t.Fatal(err)
	}
	topic := kernel.ID{2}
	r := httptest.NewRequest(http.MethodGet, view.ConversationURL("acme", kernel.ID{1}, &topic)+"/overview", nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("page status = %d", w.Code)
	}
	doc, err := html.Parse(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	branch := func(n *html.Node) bool {
		for d := range n.Descendants() {
			if d.Type == html.ElementNode && d.Data == "use" && strings.HasSuffix(attr(d, "href"), "#branch-regular") {
				return true
			}
		}
		return false
	}
	within := func(n *html.Node, id string) bool {
		for a := n; a != nil; a = a.Parent {
			if attr(a, "id") == id {
				return true
			}
		}
		return false
	}
	topicURL := view.ConversationURL("acme", kernel.ID{1}, &topic)
	var sidebar, panel, header bool
	for n := range doc.Descendants() {
		switch {
		case n.DataAtom == atom.A && attr(n, "href") == topicURL && within(n, "sidebar-topics"):
			sidebar = branch(n)
		case n.DataAtom == atom.A && attr(n, "href") == topicURL && within(n, "side-panel"):
			panel = branch(n)
		case n.DataAtom == atom.H1 && n.Parent != nil:
			for s := n.PrevSibling; s != nil && !header; s = s.PrevSibling {
				header = s.Type == html.ElementNode && s.DataAtom == atom.Svg && branch(s)
			}
		}
	}
	if !sidebar || !panel || !header {
		t.Fatalf("branch icon: sidebar row %v, overview link %v, topic header %v; want all", sidebar, panel, header)
	}
}

// listedTopic lists the viewed topic, so the sidebar and overview name it.
type listedTopic struct{ fakeMessages }

func (f listedTopic) Overview(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64, _ int64) (conversation.ChannelPage, error) {
	return f.Page(ctx, m, channel, topic, before)
}

func (f listedTopic) Page(ctx context.Context, m org.Membership, id kernel.ID, topicID *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := f.fakeMessages.Page(ctx, m, id, topicID, before)
	if page.Topic != nil {
		page.Topics = []conversation.Topic{*page.Topic}
	}
	return page, err
}
