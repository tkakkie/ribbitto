package web

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// A message's actions menu (#1159) is the same in the page and in the live
// render that every member's stream shares: it depends only on the message
// and the language, and its link is root-relative.
func TestMessageActionsSharedRender(t *testing.T) {
	catalogues, err := i18n.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", lang)
		ctx := r.Context()
		catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
		member := org.Membership{Organization: org.Organization{ID: kernel.ID{1}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{5}}}
		messages := countingMessages{calls: &atomic.Int32{}}
		entry, err := messages.One(ctx, member, kernel.ID{2}, 9)
		if err != nil {
			t.Fatal(err)
		}
		var page bytes.Buffer
		if err := view.MessageItem(viewMessage("acme", entry)).Render(ctx, &page); err != nil {
			t.Fatal(err)
		}
		live, err := messageRenderer{messages: messages, membership: member, renders: newRenderCache(t.Context())}.Render(ctx, eventOf(t, kernel.ID{1}, 9, conversation.KindPosted, conversation.Moved{ChannelID: kernel.ID{2}, ToTopicID: kernel.ID{6}}))
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("message-actions-%x", entry.ID)
		fromPage, fromStream := actionsMarkup(t, page.String(), id), actionsMarkup(t, string(live.Data), id)
		link := `href="` + view.MessageURL("acme", entry.ID) + `"`
		if fromPage == "" || fromPage != fromStream || !strings.Contains(fromPage, link) || strings.Contains(fromPage, "//") {
			t.Fatalf("%s: page menu %q, stream menu %q; want equal with %s", lang, fromPage, fromStream, link)
		}
	}
}

// actionsMarkup renders the element with id and the button that opens it.
func actionsMarkup(t *testing.T, markup, id string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for n := range doc.Descendants() {
		if attr(n, "id") == id || attr(n, "popovertarget") == id {
			if err := html.Render(&b, n); err != nil {
				t.Fatal(err)
			}
		}
	}
	return b.String()
}
