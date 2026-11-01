package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/web/static"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Called for every markupCase in both languages by TestPagesMarkup.
func checkFallbackURLs(doc *html.Node) []string {
	var problems []string
	for n := range doc.Descendants() {
		for _, method := range []string{"get", "post"} {
			enhanced, ok := attrOK(n, "hx-"+method)
			if !ok {
				continue
			}
			fallback, plainMethod := "", ""
			if n.DataAtom == atom.A {
				fallback, plainMethod = attr(n, "href"), "get"
			} else {
				form := n
				// A button must submit its form without JavaScript too.
				if (n.DataAtom == atom.Button && attr(n, "type") != "button" && attr(n, "type") != "reset") ||
					(n.DataAtom == atom.Input && attr(n, "type") == "submit") {
					for form != nil && form.DataAtom != atom.Form {
						form = form.Parent
					}
				}
				if form != nil && form.DataAtom == atom.Form {
					fallback, plainMethod = attr(form, "action"), strings.ToLower(attr(form, "method"))
					if override := attr(n, "formaction"); override != "" {
						fallback = override
					}
				}
			}
			// Quote reads no state; its plain GET and private-text-ready POST
			// deliberately have different routes (#1160).
			if n.DataAtom == atom.Form && attr(n, "id") == "quote-form" {
				plain, err := url.Parse(fallback)
				if err == nil {
					channel, _, _ := strings.Cut(plain.Path, "/topics/")
					if plainMethod == "get" && method == "post" && plain.Fragment == "message-composer" && plain.RawQuery == "" && enhanced == channel+"/quote" {
						continue
					}
				}
				problems = append(problems, "quote form must GET its conversation/composer and POST its channel's chip route")
				continue
			}
			if plainMethod != method || !sameFallbackURL(enhanced, fallback) {
				problems = append(problems, fmt.Sprintf("<%s hx-%s=%q> has no matching plain HTML URL/method (got %s %q)", n.Data, method, enhanced, plainMethod, fallback))
			}
		}
	}
	return problems
}

func sameFallbackURL(enhanced, plain string) bool {
	if enhanced == "" || plain == "" {
		return false
	}
	urls := make([]string, 0, 2)
	for _, raw := range []string{enhanced, plain} {
		u, err := url.Parse(raw)
		if err != nil {
			return false
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return false
		}
		// Compare routes and query parameters structurally, without requesting
		// the URL or interpreting cursor values; paging's success path is
		// TestMessagePagingHandler's.
		u.RawQuery = query.Encode()
		u.Fragment, u.RawFragment = "", ""
		urls = append(urls, u.String())
	}
	return urls[0] == urls[1]
}

func TestCheckFallbackURLs(t *testing.T) {
	for _, tt := range []struct {
		name, markup string
		problems     int
	}{
		{"quote fallback", `<form id="quote-form" action="/organizations/acme/channels/id/topics/topic#message-composer" method="get" hx-post="/organizations/acme/channels/id/quote"></form>`, 0},
		{"quote wrong channel", `<form id="quote-form" action="/organizations/acme/channels/id#message-composer" method="get" hx-post="/organizations/acme/channels/other/quote"></form>`, 1},
		{"quote wrong method", `<form id="quote-form" action="/organizations/acme/channels/id#message-composer" method="post" hx-post="/organizations/acme/channels/id/quote"></form>`, 1},
		{"plain HTML", `<a href="/x">Open</a>`, 0},
		{"paging fixture", `<a href="/x?before=0" hx-get="/x?before=0">Older</a>`, 0},
		{"query order", `<a href="/x?a=1&amp;b=2" hx-get="/x?b=2&amp;a=1">Open</a>`, 0},
		{"form", `<form action="/x" method="POST" hx-post="/x"></form>`, 0},
		{"submit button", `<form action="/x" method="post"><button type="submit" hx-post="/x">Send</button></form>`, 0},
		{"submit override", `<form action="/x" method="post"><button type="submit" formaction="/y" hx-post="/y">Send</button></form>`, 0},
		{"wrong submit override", `<form action="/x" method="post"><button type="submit" formaction="/y" hx-post="/x">Send</button></form>`, 1},
		{"get form", `<form action="/x" method="get" hx-get="/x"></form>`, 0},
		{"missing href", `<a hx-get="/x">Open</a>`, 1},
		{"different route", `<a href="/y" hx-get="/x">Open</a>`, 1},
		{"different query", `<a href="/x?before=7" hx-get="/x?before=8">Older</a>`, 1},
		{"missing query", `<a href="/x" hx-get="/x?before=7">Older</a>`, 1},
		{"invalid query", `<a href="/x?before=%zz" hx-get="/x?before=%zz">Older</a>`, 1},
		{"missing action", `<form method="post" hx-post="/x"></form>`, 1},
		{"missing method", `<form action="/x" hx-post="/x"></form>`, 1},
		{"wrong method", `<form action="/x" method="get" hx-post="/x"></form>`, 1},
		{"post link", `<a href="/x" hx-post="/x">Send</a>`, 1},
		{"inert element", `<div href="/x" hx-get="/x"></div>`, 1},
		{"non-submit button", `<form action="/x" method="post"><button type="button" hx-post="/x">Send</button></form>`, 1},
		{"every enhanced element", `<a hx-get="/x">Open</a><form hx-post="/y"></form>`, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(tt.markup))
			if err != nil {
				t.Fatal(err)
			}
			if problems := checkFallbackURLs(doc); len(problems) != tt.problems {
				t.Fatalf("problems %q, want %d", problems, tt.problems)
			}
		})
	}
}

func assertFullConversationPage(t *testing.T, body string) {
	t.Helper()
	// html.Parse synthesizes document wrappers for fragments, so check the
	// response bytes for the layout before checking the conversation itself.
	for _, want := range []string{"<!doctype html>", "<html ", "<head>", "<title>", "<body ", `id="conversation"`, "</body>", "</html>"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("full conversation page missing %q", want)
		}
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	composerURL, err := static.ScriptURL(t.Context(), "composer")
	if err != nil {
		t.Fatal(err)
	}
	composer := false
	var scripts []string
	for n := range doc.Descendants() {
		if n.DataAtom != atom.Script {
			continue
		}
		src := attr(n, "src")
		scripts = append(scripts, src)
		if src == composerURL {
			composer = true
		} else if strings.HasPrefix(src, "/static/dist/composer-") || strings.HasPrefix(src, "/static/message-composer-") {
			t.Errorf("unexpected composer script %q", src)
		}
	}
	order := strings.Join(scripts, " ")
	if strings.Index(order, composerURL) > strings.Index(order, "/static/message-stream-v14.js") || strings.Index(order, "/static/message-stream-v14.js") > strings.Index(order, "/static/message-drafts-v7.js") {
		t.Errorf("composer listener registration order: %s", order)
	}
	if !composer {
		t.Errorf("conversation page missing %s", composerURL)
	}
}
