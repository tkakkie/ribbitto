package web

import (
	"bytes"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// bodyMarkup is every element and attribute view's messageBody, bodyInlines
// and MemberName write inside a body. Anything else in a rendered body came
// from the author, so a template change that adds one must add it here.
var bodyMarkup = map[string]map[string]bool{
	"div":        {"id": true, "dir": true, "class": true},
	"p":          {"id": true, "dir": true, "class": true},
	"pre":        {"dir": true, "class": true},
	"code":       {"dir": true, "class": true},
	"blockquote": {"dir": true, "class": true},
	"strong":     {"dir": true},
	"em":         {"dir": true},
	"a":          {"href": true, "rel": true, "dir": true, "class": true},
	"bdi":        {"data-quote-handle": true, "data-member-id": true, "class": true},
	"span":       {"class": true},
	"br":         {},
}

// bodyDir is the elements the templates always give dir="auto".
var bodyDir = map[string]bool{
	"div": true, "p": true, "pre": true, "code": true, "blockquote": true, "strong": true, "em": true, "a": true,
}

// FuzzMessageBodyHTML renders a chat_v1 body as the channel page and the
// stream do (viewMessage, then view.MessageItem) and parses the HTML back.
// A body is the one place an author's text reaches every member's page, so
// no body may produce an element or attribute the templates do not write, an
// href other than its own http(s) link text, or text other than its
// extraction (decision 39) with each reference shown by name.
func FuzzMessageBodyHTML(f *testing.F) {
	member := kernel.ID{0xaa, 1}
	ref := "<@" + realtime.FormatPayloadID(member) + ">"
	for _, body := range []string{
		"<script>alert(1)</script>", "**bold** _it_ `code`", "https://e.org/\"onmouseover=x", "> quote\nline",
		"```go\n<b>x</b>\n```", "hi " + ref + " there", "<@" + realtime.FormatPayloadID(kernel.ID{0xbb}) + ">",
		"https://a.b/c?d=<e>&f='g'", "(https://x.y/(z))", "javascript:alert(1)", "http://[::1]/", "**a _b** c_",
		"https://e.org/\u2066x", "&lt;b&gt;", "<!-- x -->", "\\<b\\>", "]]><![CDATA[",
	} {
		f.Add(body, "Tom <i>", "tomoya")
	}
	f.Fuzz(func(t *testing.T, body, displayName, handle string) {
		body, err := conversation.ValidateMessageBody(body)
		if err != nil {
			return
		}
		// Stored handles pass ValidateHandle and display names hold no
		// control characters, which the HTML parser would rewrite. An empty
		// handle stands for a member the directory did not return.
		if handle != "" {
			if handle, err = org.ValidateHandle(handle); err != nil {
				return
			}
		}
		if !utf8.ValidString(displayName) || strings.ContainsFunc(displayName, unicode.IsControl) {
			return
		}
		blocks := conversation.ParseChatV1(body)
		refs := map[kernel.ID]conversation.Reference{}
		if handle != "" {
			refs[member] = conversation.Reference{DisplayName: displayName, Handle: handle}
		}
		entry := conversation.Entry{
			Message:    conversation.Message{ID: kernel.ID{1}, ChannelID: kernel.ID{2}, TopicID: kernel.ID{3}, Format: conversation.FormatChatV1, Body: body},
			Blocks:     blocks,
			References: refs,
		}
		var out bytes.Buffer
		if err := view.MessageItem(viewMessage("org", entry)).Render(t.Context(), &out); err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(&out)
		if err != nil {
			t.Fatal(err)
		}
		var rendered *html.Node
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode && attr(n, "id") == "body-"+view.MessageDOMID(entry.ID) {
				rendered = n
				break
			}
		}
		if rendered == nil {
			t.Fatalf("body %q: no body element in %q", body, out.String())
		}
		// The body element itself is the template's too, so it is held to the
		// same allow-list as what it contains.
		nodes := []*html.Node{rendered}
		for n := range rendered.Descendants() {
			nodes = append(nodes, n)
		}
		for _, n := range nodes {
			if n.Type == html.TextNode {
				continue
			}
			allowed, ok := bodyMarkup[n.Data]
			if n.Type != html.ElementNode || !ok {
				t.Fatalf("body %q renders node %d %q", body, n.Type, n.Data)
			}
			// A missing attribute is as much a regression as an extra one: every
			// element below carries dir="auto" (the <code> inside <pre> is the one
			// that does not).
			if bodyDir[n.Data] && !(n.Data == "code" && n.Parent != nil && n.Parent.Data == "pre") && attr(n, "dir") != "auto" {
				t.Fatalf("body %q renders <%s> without dir=\"auto\"", body, n.Data)
			}
			for _, a := range n.Attr {
				if !allowed[a.Key] {
					t.Fatalf("body %q renders %s=%q on <%s>", body, a.Key, a.Val, n.Data)
				}
				if a.Key == "href" && (!strings.HasPrefix(a.Val, "https://") && !strings.HasPrefix(a.Val, "http://") || a.Val != text(n)) {
					t.Fatalf("body %q renders href %q for link text %q", body, a.Val, text(n))
				}
			}
		}
		var want strings.Builder
		for _, segment := range conversation.ExtractChatV1(blocks) {
			switch reference, ok := refs[segment.Member]; {
			case !segment.Reference:
				want.WriteString(segment.Text)
			case ok:
				if name := memberDisplayName(reference.DisplayName); name != "" {
					want.WriteString(name + " ")
				}
				want.WriteString("@" + reference.Handle)
			default:
				want.WriteString(view.UnknownMember(t.Context()))
			}
		}
		// Line and block breaks are <br> and block elements, not text.
		if got, w := strings.ReplaceAll(text(rendered), "\n", ""), strings.ReplaceAll(want.String(), "\n", ""); got != w {
			t.Fatalf("body %q\n text %q\n want %q", body, got, w)
		}
	})
}
