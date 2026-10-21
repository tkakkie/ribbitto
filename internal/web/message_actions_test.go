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

// Only message actions omit type to avoid htmx's explicit-submit selector.
// Each branch repeats the data the real markup in view/message.templ renders,
// so an unrelated untyped button that shares one attribute is still reported.
func messageSubmitButton(n *html.Node) bool {
	if n.Data != "button" {
		return false
	}
	if attr(n, "form") == "quote-form" && attr(n, "name") == "quote" {
		if attr(n, "value") == "" {
			return false
		}
		// The hidden Quote-selection button also names the body it describes.
		if feedHas(n, "data-quote-selection") {
			return feedHas(n, "hidden") && attr(n, "aria-describedby") != ""
		}
		return true
	}
	// A pin control's two buttons, each with the route it submits to.
	pinned := attr(n, "data-pin-action")
	return (pinned == "true" || pinned == "false") && attr(n, "formaction") != "" &&
		n.Parent != nil && n.Parent.Data == "form" && feedHas(n.Parent, "data-pin-control")
}

func plainMessageSubmit(n *html.Node) bool {
	_, typed := attrOK(n, "type")
	return !typed && !hasHTMXAttribute(n)
}

func TestMessageSubmitMarkup(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, lang := range []string{"en", "ja"} {
			entry := view.Message{ID: kernel.ID{8}, Pinned: pinned, Body: "original"}
			doc, err := html.Parse(strings.NewReader(renderIn(t, lang, view.MessageItem(entry))))
			if err != nil {
				t.Fatal(err)
			}
			submits, triggers, quotes := 0, 0, 0
			for n := range doc.Descendants() {
				if attr(n, "popovertarget") != "" {
					triggers++
					if attr(n, "type") != "button" {
						t.Fatal("popover trigger lost type=button")
					}
				}
				if !messageSubmitButton(n) {
					continue
				}
				submits++
				if !plainMessageSubmit(n) {
					t.Fatal("message submit button has type or hx-* attributes")
				}
				if attr(n, "name") == "quote" && !feedHas(n, "data-quote-selection") {
					quotes++
					label := map[string]string{"en": "Quote", "ja": "引用"}[lang]
					if len(n.Attr) != 4 || attr(n, "form") != "quote-form" || attr(n, "value") != "08000000-0000-0000-0000-000000000000" || text(n) != label || attr(n, "class") != "flex min-h-11 w-full items-center rounded-md px-3 hover:bg-hover" {
						t.Fatal("menu Quote contract changed")
					}
				}
				// Mutate each rendered action: the same assertion must reject
				// restoring explicit submit initialization or request attributes.
				for _, key := range []string{"type", "hx-post"} {
					n.Attr = append(n.Attr, html.Attribute{Key: key, Val: "submit"})
					if plainMessageSubmit(n) || !strings.Contains(strings.Join(checkMarkup(doc, false), "\n"), "message submit button has type or hx-* attributes") {
						t.Fatalf("attribute assertion accepted %s on rendered action", key)
					}
					n.Attr = n.Attr[:len(n.Attr)-1]
				}
			}
			if submits != 4 || triggers != 1 || quotes != 1 {
				t.Fatalf("submits/triggers/menu quotes = %d/%d/%d, want 4/1/1", submits, triggers, quotes)
			}
		}
	}
}

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
