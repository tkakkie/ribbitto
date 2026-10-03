package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// checkMarkup reports what breaks the rules in docs/accessibility.md. Full pages get the document checks; fragments and
// components only the element checks.
// appName is the English and Japanese app.title, which alone names no page.
const appName = "ribbitto"

func checkMarkup(doc *html.Node, fullPage bool) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	labelled := map[string]bool{} // ids named by <label for>
	ids := map[string]bool{}
	// Collect targets first so references may point forward in the document.
	for n := range doc.Descendants() {
		if id, ok := attrOK(n, "id"); ok {
			if ids[id] {
				add("duplicate id %q", id)
			}
			ids[id] = true
		}
		if n.DataAtom == atom.Label && attr(n, "for") != "" {
			labelled[attr(n, "for")] = true
		}
	}
	// HTML permits a space separator, optional seconds and compact offsets;
	// RFC3339 alone would both reject valid HTML and accept invalid fractions.
	// https://html.spec.whatwg.org/multipage/common-microsyntaxes.html#global-dates-and-times
	dateTime := regexp.MustCompile(`^([0-9]{4,})(-[0-9]{2}-[0-9]{2})[T ]` +
		`(?:[01][0-9]|2[0-3]):[0-5][0-9](?::[0-5][0-9](?:\.[0-9]{1,3})?)?` +
		`(Z|[+-](?:[01][0-9]|2[0-3]):?[0-5][0-9])$`)
	asciiSpace := func(r rune) bool { return strings.ContainsRune(" \t\n\r\f", r) }
	mains, lastHeading := 0, 0
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		for _, a := range n.Attr {
			if a.Key == "style" || strings.HasPrefix(a.Key, "on") {
				add("<%s> has a %s attribute", n.Data, a.Key)
			}
			switch a.Key {
			case "for", "aria-describedby", "aria-labelledby":
				targets := []string{a.Val}
				if a.Key != "for" || n.DataAtom == atom.Output {
					targets = strings.FieldsFunc(a.Val, asciiSpace)
				}
				for _, id := range targets {
					if id != "" && !ids[id] {
						add("<%s %s=%q> references missing id %q", n.Data, a.Key, a.Val, id)
					}
				}
			}
		}
		// An <a> is interactive only with href: without it, it is not
		// focusable and has no keyboard activation.
		_, hasHref := attrOK(n, "href")
		interactive := n.DataAtom == atom.Button || n.DataAtom == atom.A && hasHref || n.DataAtom == atom.Input || n.DataAtom == atom.Select || n.DataAtom == atom.Textarea
		if attr(n, "role") == "button" && !interactive {
			add("<%s role=button> simulates a button", n.Data)
		}
		if tabindex, ok := attrOK(n, "tabindex"); ok && tabindex != "-1" && (tabindex != "0" || attr(n, "role") != "region" || attr(n, "aria-label") == "") {
			add("<%s tabindex=%q> is neither a focus target (-1) nor a labelled scrollable region (0)", n.Data, tabindex)
		}
		switch n.DataAtom {
		case atom.Time:
			if value, ok := attrOK(n, "datetime"); ok {
				parts := dateTime.FindStringSubmatch(value)
				valid := false
				if parts != nil && strings.Trim(parts[1], "0") != "" && parts[3] != "-00:00" && parts[3] != "-0000" {
					// Go parses four-digit years. Gregorian leap years repeat
					// every 400 years, so the last four digits suffice here.
					year := parts[1]
					_, err := time.Parse(time.DateOnly, year[len(year)-4:]+parts[2])
					valid = err == nil
				}
				if !valid {
					add("<time datetime=%q> is not a valid HTML global date and time", value)
				}
			}
		case atom.Main:
			mains++
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			level := int(n.Data[1] - '0')
			if fullPage && level > lastHeading+1 {
				add("<%s> skips a heading level after h%d", n.Data, lastHeading)
			}
			lastHeading = level
		case atom.Button:
			if attr(n, "type") == "" {
				add("<button> without a type")
			}
			if strings.TrimSpace(text(n)) == "" && strings.TrimSpace(attr(n, "aria-label")) == "" {
				add("<button> without an accessible name")
			}
		case atom.Img:
			if _, ok := attrOK(n, "alt"); !ok {
				add("<img> without alt")
			}
		case atom.Input, atom.Select, atom.Textarea:
			switch attr(n, "type") {
			case "hidden", "submit", "button", "reset", "image":
				continue
			}
			if !labelled[attr(n, "id")] && !implicitlyLabelled(n) {
				add("<%s name=%q> without a label", n.Data, attr(n, "name"))
			}
		}
	}
	if fullPage {
		if root := find(doc, atom.Html); root == nil || attr(root, "lang") == "" {
			add("<html> without lang")
		}
		if mains != 1 {
			add("%d <main> elements, want 1", mains)
		}
		// WCAG 2.4.2: tabs, history and screen readers tell pages apart by it.
		if title := find(doc, atom.Title); title == nil || strings.TrimSpace(text(title)) == "" || strings.TrimSpace(text(title)) == appName {
			add("<title> missing, empty or only the app name")
		}
	}
	return problems
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

// text is the text that can name an element: aria-hidden or hidden
// subtrees are left out, as the accessible-name computation does.
func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if _, hidden := attrOK(c, "hidden"); c.Type == html.ElementNode && (attr(c, "aria-hidden") == "true" || hidden) {
				continue
			}
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			}
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// implicitlyLabelled reports whether a wrapping <label> labels n: only a
// label without for, and only for its first labelable descendant.
func implicitlyLabelled(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.DataAtom != atom.Label {
			continue
		}
		if _, ok := attrOK(p, "for"); ok {
			return false
		}
		for d := range p.Descendants() {
			if labelable(d) {
				return d == n
			}
		}
	}
	return false
}

func labelable(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Button, atom.Meter, atom.Output, atom.Progress, atom.Select, atom.Textarea:
		return true
	case atom.Input:
		return attr(n, "type") != "hidden"
	}
	return false
}

func find(doc *html.Node, a atom.Atom) *html.Node {
	for n := range doc.Descendants() {
		if n.DataAtom == a {
			return n
		}
	}
	return nil
}

// idAttribute reads key from the element with id, failing the test when
// there is no such element: the DOM contract names both.
func idAttribute(t *testing.T, doc *html.Node, id, key string) (string, bool) {
	t.Helper()
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && attr(n, "id") == id {
			return attrOK(n, key)
		}
	}
	t.Fatalf("no element with id %q", id)
	return "", false
}

// The checker itself: each rule catches what it should and nothing more.
func TestCheckMarkup(t *testing.T) {
	page := func(body string) string {
		return `<!DOCTYPE html><html lang="en"><head><title>t</title></head><body>` + body + `</body></html>`
	}
	for _, tt := range []struct {
		name, markup string
		fullPage     bool
		want         []string // one substring per problem; nil for none
	}{
		{"valid page", page(`<main><h1>a</h1><h2>b</h2><form><label>Email <input type="email" name="email"></label><label for="p">Password</label><input id="p" type="password" name="p"><input type="hidden" name="t"><button type="submit">Go</button></form><img src="x" alt=""></main>`), true, nil},
		{"no lang", `<!DOCTYPE html><html><head><title>t</title></head><body><main></main></body></html>`, true, []string{"without lang"}},
		{"no title", `<!DOCTYPE html><html lang="en"><body><main></main></body></html>`, true, []string{"<title>"}},
		{"blank title", `<!DOCTYPE html><html lang="en"><head><title> </title></head><body><main></main></body></html>`, true, []string{"<title>"}},
		{"title is only the app name", `<!DOCTYPE html><html lang="en"><head><title>ribbitto</title></head><body><main></main></body></html>`, true, []string{"<title>"}},
		{"two mains", page(`<main></main><main></main>`), true, []string{"2 <main>"}},
		{"skipped heading", page(`<main><h1>a</h1><h3>b</h3></main>`), true, []string{"skips a heading level"}},
		{"unlabelled input", page(`<main><input type="text" name="q"></main>`), true, []string{"without a label"}},
		{"button without type", page(`<main><button>Go</button></main>`), true, []string{"without a type"}},
		{"button without name", page(`<main><button type="button"> </button></main>`), true, []string{"accessible name"}},
		{"img without alt", page(`<main><img src="x"></main>`), true, []string{"without alt"}},
		{"style attribute", page(`<main style="color:red"></main>`), true, []string{"style attribute"}},
		{"inline handler", page(`<main><a href="/" onclick="x()">a</a></main>`), true, []string{"onclick attribute"}},
		{"simulated button", page(`<main><div role="button">Go</div></main>`), true, []string{"simulates a button"}},
		{"link without href as a button", page(`<main><a role="button" hx-post="/x">Go</a></main>`), true, []string{"simulates a button"}},
		{"icon button named only by hidden text", page(`<main><button type="button"><span aria-hidden="true">×</span></button></main>`), true, []string{"accessible name"}},
		{"blank aria-label", page(`<main><button type="button" aria-label=" "></button></main>`), true, []string{"accessible name"}},
		{"icon button with a name", page(`<main><button type="button" aria-label="Close"><span aria-hidden="true">×</span></button></main>`), true, nil},
		// An empty for names no control, whether the label wraps it or not.
		{"empty for beside the input", page(`<main><label for="">Email</label><input type="text" name="email"></main>`), true, []string{"without a label"}},
		{"empty for on a wrapping label", page(`<main><label for="">Email <input type="text" name="email"></label></main>`), true, []string{"without a label"}},
		{"wrapping label pointing elsewhere", page(`<main><label for="missing">Email <input id="email" type="email" name="email"></label></main>`), true, []string{`references missing id "missing"`, "without a label"}},
		{"second control in one label", page(`<main><label>Name <input type="text" name="a"><input type="text" name="b"></label></main>`), true, []string{"name=\"b\""}},
		{"stray tabindex", page(`<main><div tabindex="0">x</div></main>`), true, []string{"tabindex"}},
		{"focus target", page(`<main><h1 tabindex="-1">a</h1><div role="region" aria-label="Messages" tabindex="0">x</div></main>`), true, nil},
		{"fragment skips document checks", `<form><button type="submit">Go</button></form>`, false, nil},
		{"duplicate ids", `<p id="same"></p><span id="same"></span>`, false, []string{`duplicate id "same"`}},
		{"unique ids", `<p id="one"></p><span id="two"></span>`, false, nil},
		{"missing for target", `<label for="missing">Name</label>`, false, []string{`references missing id "missing"`}},
		{"forward for target", `<label for="name">Name</label><input id="name">`, false, nil},
		{"output for targets", `<output for="a b"></output><input id="a" type="hidden"><input id="b" type="hidden">`, false, nil},
		{"missing description target", `<p aria-describedby="present missing"></p><p id="present"></p>`, false, []string{`aria-describedby="present missing"> references missing id "missing"`}},
		{"missing naming target", `<p aria-labelledby="present missing"></p><p id="present"></p>`, false, []string{`aria-labelledby="present missing"> references missing id "missing"`}},
		{"multiple forward ARIA targets", "<p aria-describedby=\"first\tsecond\nthird\" aria-labelledby=\"third second\"></p><p id=\"first\"></p><p id=\"second\"></p><p id=\"third\"></p>", false, nil},
		{"backward ARIA targets", `<p id="a"></p><p id="b"></p><p aria-describedby="a b" aria-labelledby="b a"></p>`, false, nil},
		{"UTC datetime", `<time datetime="2026-09-30T12:34:56Z"></time>`, false, nil},
		{"fractional datetime", `<time datetime="2026-09-30T12:34:56.123Z"></time>`, false, nil},
		{"tenths datetime", `<time datetime="2026-09-30T12:34:56.1Z"></time>`, false, nil},
		{"hundredths datetime", `<time datetime="2026-09-30T12:34:56.12Z"></time>`, false, nil},
		{"space and omitted seconds", `<time datetime="2026-09-30 12:34Z"></time>`, false, nil},
		{"positive offset", `<time datetime="2026-09-30T12:34:56+09:00"></time>`, false, nil},
		{"negative compact offset", `<time datetime="2026-09-30T12:34:56-0530"></time>`, false, nil},
		{"zero offset", `<time datetime="2026-09-30T12:34:56+00:00"></time>`, false, nil},
		{"leap day", `<time datetime="2000-02-29T00:00Z"></time>`, false, nil},
		{"expanded year", `<time datetime="10000-02-29T00:00Z"></time>`, false, nil},
		{"time without datetime is not checked", `<time>12:34</time>`, false, nil},
		{"empty datetime", `<time datetime=""></time>`, false, []string{"not a valid HTML global date and time"}},
		{"Go timestamp", `<time datetime="2026-09-30 12:34:56 +0000 UTC"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"date only", `<time datetime="2026-09-30"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"missing timezone", `<time datetime="2026-09-30T12:34:56"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid leap day", `<time datetime="1900-02-29T00:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid month", `<time datetime="2026-13-01T00:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid day", `<time datetime="2026-04-31T00:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"zero year", `<time datetime="0000-01-01T00:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"short year", `<time datetime="999-01-01T00:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid hour", `<time datetime="2026-09-30T24:00Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid minute", `<time datetime="2026-09-30T12:60Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"leap second", `<time datetime="2026-09-30T12:34:60Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"excess fractional precision", `<time datetime="2026-09-30T12:34:56.1234Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"comma fraction", `<time datetime="2026-09-30T12:34:56,123Z"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid offset hour", `<time datetime="2026-09-30T12:34:56+24:00"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"invalid offset minute", `<time datetime="2026-09-30T12:34:56+00:60"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"negative zero offset", `<time datetime="2026-09-30T12:34:56-00:00"></time>`, false, []string{"not a valid HTML global date and time"}},
		{"trailing whitespace", `<time datetime="2026-09-30T12:34:56Z "></time>`, false, []string{"not a valid HTML global date and time"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(tt.markup))
			if err != nil {
				t.Fatal(err)
			}
			problems := checkMarkup(doc, tt.fullPage)
			if len(problems) != len(tt.want) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
			for i, want := range tt.want {
				if !strings.Contains(problems[i], want) {
					t.Errorf("problem %d = %q, want substring %q", i, problems[i], want)
				}
			}
		})
	}
}

// Match the CSS subset used by our DOM contract. Unknown syntax fails closed:
// extend this helper and its tests when templates need another selector shape.
func htmxMatches(doc *html.Node, selector string) ([]*html.Node, error) {
	parts := regexp.MustCompile(`^#([A-Za-z][A-Za-z0-9_-]*)(?:\s*>\s*([a-z][a-z0-9-]*))?$`).FindStringSubmatch(strings.TrimSpace(selector))
	if parts == nil {
		return nil, fmt.Errorf("unsupported selector %q", selector)
	}
	var matches []*html.Node
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || attr(n, "id") != parts[1] {
			continue
		}
		if parts[2] == "" {
			matches = append(matches, n)
			continue
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == parts[2] {
				matches = append(matches, child)
			}
		}
	}
	return matches, nil
}

func checkHTMXContract(page, element, response *html.Node) []string {
	var problems []string
	for _, key := range []string{"hx-target", "hx-select", "hx-select-oob"} {
		value, exists := attrOK(element, key)
		if !exists {
			continue
		}
		for _, selector := range strings.Split(value, ",") {
			selector = strings.TrimSpace(selector)
			// Extended non-id targets are allowed: this, closest …, find ….
			// They need no fixed destination id; any ids they name still exist.
			if key == "hx-target" && (selector == "this" || strings.HasPrefix(selector, "closest ") || strings.HasPrefix(selector, "find ")) {
				for _, id := range regexp.MustCompile(`#[A-Za-z][A-Za-z0-9_-]*`).FindAllString(selector, -1) {
					matches, err := htmxMatches(page, id)
					if err != nil || len(matches) == 0 {
						problems = append(problems, fmt.Sprintf("hx-target %q: missing page id %s", selector, id))
					}
				}
				continue
			}
			doc, side := response, "response"
			if key == "hx-target" {
				doc, side = page, "page"
			}
			matches, err := htmxMatches(doc, selector)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", key, err))
			} else if len(matches) == 0 {
				problems = append(problems, fmt.Sprintf("%s %q: no match in %s", key, selector, side))
			}
			if key == "hx-select-oob" {
				for _, match := range matches {
					id := attr(match, "id")
					destinations, err := htmxMatches(page, "#"+id)
					if err != nil || len(destinations) == 0 {
						problems = append(problems, fmt.Sprintf("hx-select-oob %q: missing page destination id %q", selector, id))
					}
				}
			}
		}
	}
	return problems
}

func TestCheckHTMXContract(t *testing.T) {
	for _, tt := range []struct {
		name, attributes, page, response, want string
	}{
		{"response-only selection", `hx-target="#container" hx-select="#result"`, `<div id="container"></div>`, `<div id="result"></div>`, ""},
		{"missing target", `hx-target="#missing"`, `<div id="container"></div>`, `<div id="missing"></div>`, "no match in page"},
		{"missing selection", `hx-select="#result"`, `<div id="result"></div>`, `<div></div>`, "no match in response"},
		{"direct children", `hx-select="#items > li"`, "", `<ol id="items"><li></li></ol>`, ""},
		{"empty list", `hx-select="#items > li"`, "", `<ol id="items"></ol>`, "no match in response"},
		{"nested child is not direct", `hx-select="#items > li"`, "", `<div id="items"><ol><li></li></ol></div>`, "no match in response"},
		{"oob match", `hx-select-oob="#older"`, `<div id="older"></div>`, `<div id="older"></div>`, ""},
		{"missing oob response", `hx-select-oob="#older"`, `<div id="older"></div>`, "", "no match in response"},
		{"missing oob destination", `hx-select-oob="#older"`, "", `<div id="older"></div>`, "missing page destination"},
		{"every selector", `hx-select-oob="#older, #missing"`, `<div id="older"></div>`, `<div id="older"></div>`, "no match in response"},
		{"this", `hx-target="this"`, "", "", ""},
		{"closest", `hx-target="closest form"`, "", "", ""},
		{"find", `hx-target="find textarea"`, "", "", ""},
		{"extended target id", `hx-target="closest #missing"`, "", "", "missing page id"},
		{"unsupported syntax", `hx-select=".result"`, "", "", "unsupported selector"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parse := func(markup string) *html.Node {
				t.Helper()
				doc, err := html.Parse(strings.NewReader(markup))
				if err != nil {
					t.Fatal(err)
				}
				return doc
			}
			element := find(parse(`<button `+tt.attributes+`></button>`), atom.Button)
			problems := checkHTMXContract(parse(tt.page), element, parse(tt.response))
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

// Exercise only explicit htmx requests. Plain links/forms belong to #196.
func checkHTMXRequests(t *testing.T, handler http.Handler, page *html.Node, lang string, cookie bool) {
	t.Helper()
	for element := range page.Descendants() {
		for _, method := range []string{"GET", "POST"} {
			path, exists := attrOK(element, "hx-"+strings.ToLower(method))
			if !exists {
				continue
			}
			t.Run(method+" "+path, func(t *testing.T) {
				form := url.Values{}
				for n := range element.Descendants() {
					name := attr(n, "name")
					if name == "" {
						continue
					}
					switch n.DataAtom {
					case atom.Select:
						form.Add(name, "")
					case atom.Textarea:
						form.Add(name, text(n))
					case atom.Input:
						form.Add(name, attr(n, "value"))
					default:
						t.Fatalf("add htmx form fixture support for <%s name=%q>", n.Data, name)
					}
				}
				// Check the rendered draft (including validation errors), then a
				// successful post: both responses must satisfy the same selectors.
				for _, submit := range []bool{false, true} {
					if submit {
						if method != "POST" || !form.Has("body") {
							continue
						}
						form.Set("body", "DOM contract test")
					}
					t.Run(fmt.Sprintf("submit=%t", submit), func(t *testing.T) {
						r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						r.Header.Set("Accept-Language", lang)
						r.Header.Set("HX-Request", "true")
						if cookie {
							r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
						}
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, r)
						// Only the composer's unsubmitted draft POST may be invalid (422);
						// paging GETs and valid posts must succeed.
						draftPost := method == "POST" && !submit
						if (w.Code != http.StatusOK && (!draftPost || w.Code != http.StatusUnprocessableEntity)) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
							t.Fatalf("HX response: status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
						}
						response, err := html.Parse(w.Body)
						if err != nil {
							t.Fatal(err)
						}
						for _, problem := range checkHTMXContract(page, element, response) {
							t.Error(problem)
						}
					})
				}
			})
		}
	}
}

// markupCase is one rendered state of an HTML route.
type markupCase struct {
	name          string
	route         string // the registered pattern it exercises
	services      func() Services
	method        string
	path          string
	form          url.Values
	cookie        bool
	repeat        int // send the request this many times and check the last
	status        int // the expected status; 0 means 200
	htmx          bool
	alerts        int      // when set, the number of role="alert" errors the page must show
	invalidFields []string // setup/sign-up inputs with submit errors
}

// Every page in every state, in both languages, passes checkMarkup, and
// every registered HTML route is either rendered here or listed as not
// rendering a page.
func TestPagesMarkup(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	fieldErrors := setup.ValidationErrors{}
	for _, field := range []string{"organization_name", "slug", "display_name", "handle", "email", "password"} {
		fieldErrors[field] = errors.New("invalid")
	}
	base := func() Services {
		return testServices()
	}
	withSetup := func(err error) func() Services {
		return func() Services {
			s := base()
			f := &fakeSetup{open: true, err: err}
			s.Setup, s.SetupSessions = f, f
			return s
		}
	}
	withSignUp := func(open bool, err error) func() Services {
		return func() Services {
			s := base()
			f := &fakeSetup{open: open, err: err}
			s.SignUp, s.SetupSessions = fakeSignUp{f}, f
			return s
		}
	}
	signedIn := func(authorizer Authorizer) func() Services {
		return func() Services {
			s := base()
			s.Sessions, s.Authz = oneSession{}, authorizer
			return s
		}
	}
	withChannelError := func(err error) func() Services {
		return func() Services {
			s := signedIn(oneOrganisation{})()
			s.Channels = &fakeChannels{createErr: err}
			return s
		}
	}
	limited := func() Services {
		s := withSignUp(true, nil)()
		now := func() time.Time { return time.Unix(0, 0) }
		s.Limits = &middleware.AuthLimits{
			SignIn: middleware.NewRateLimiter(middleware.Limit{Burst: 1, Every: time.Hour}, now),
			SignUp: middleware.NewRateLimiter(middleware.Limit{Burst: 1, Every: time.Hour}, now),
			Setup:  middleware.NewRateLimiter(middleware.Limit{Burst: 1, Every: time.Hour}, now),
		}
		return s
	}
	setupForm := url.Values{"token": {"t"}, "organization_name": {"Org"}, "slug": {"org"}, "display_name": {"Owner"}, "handle": {"owner"}, "email": {"a@b"}, "password": {"p"}}
	cases := []markupCase{
		{name: "home signed out", route: "GET /{$}", services: base, method: "GET", path: "/"},
		{name: "home signed out, sign-up open", route: "GET /{$}", services: withSignUp(true, nil), method: "GET", path: "/"},
		{name: "home signed out, sign-up closed", route: "GET /{$}", services: withSignUp(false, nil), method: "GET", path: "/"},
		{name: "home signed in", route: "GET /{$}", services: signedIn(noOrganisations{}), method: "GET", path: "/", cookie: true},
		{name: "sign-in", route: "GET /signin", services: base, method: "GET", path: "/signin"},
		{name: "sign-in, sign-up open", route: "GET /signin", services: withSignUp(true, nil), method: "GET", path: "/signin"},
		{name: "sign-in, sign-up closed", route: "GET /signin", services: withSignUp(false, nil), method: "GET", path: "/signin"},
		{name: "sign-in failed", route: "POST /signin", services: func() Services { s := base(); s.SignIn = &fakeSignIn{err: auth.ErrInvalidCredentials}; return s }, method: "POST", path: "/signin", form: url.Values{"email": {"a@b"}, "password": {"p"}}, status: http.StatusUnprocessableEntity},
		{name: "sign-in rate-limited", route: "POST /signin", services: limited, method: "POST", path: "/signin", form: url.Values{"email": {"a@b"}, "password": {"p"}}, repeat: 2, status: http.StatusTooManyRequests},
		{name: "setup", route: "GET /setup", services: withSetup(nil), method: "GET", path: "/setup"},
		{name: "setup, every field invalid", route: "POST /setup", services: withSetup(fieldErrors), method: "POST", path: "/setup", form: setupForm, status: http.StatusUnprocessableEntity, alerts: 6, invalidFields: []string{"organization_name", "slug", "display_name", "handle", "email", "password"}},
		{name: "setup, wrong token", route: "POST /setup", services: withSetup(setup.ErrToken), method: "POST", path: "/setup", form: setupForm, status: http.StatusUnprocessableEntity, alerts: 1, invalidFields: []string{"token"}},
		{name: "sign-up", route: "GET /signup", services: withSignUp(true, nil), method: "GET", path: "/signup"},
		{name: "sign-up, every field invalid", route: "POST /signup", services: withSignUp(true, fieldErrors), method: "POST", path: "/signup", form: setupForm, status: http.StatusUnprocessableEntity, alerts: 4, invalidFields: []string{"display_name", "handle", "email", "password"}},
		{name: "channel", route: "GET /organizations/{slug}/channels/{channelID}", services: signedIn(oneOrganisation{}), method: "GET", path: view.ChannelURL("acme", domain.ID{1}), cookie: true},
		{name: "channel with messages", route: "GET /organizations/{slug}/channels/{channelID}", services: func() Services { s := signedIn(oneOrganisation{})(); s.Messages = populatedMessages(); return s }, method: "GET", path: view.ChannelURL("acme", domain.ID{1}), cookie: true},
		{name: "channel with older messages", route: "GET /organizations/{slug}/channels/{channelID}", services: func() Services { s := signedIn(oneOrganisation{})(); s.Messages = olderMessages(); return s }, method: "GET", path: view.ChannelURL("acme", domain.ID{1}), cookie: true},
		{name: "older page", route: "GET /organizations/{slug}/channels/{channelID}", services: func() Services { s := signedIn(oneOrganisation{})(); s.Messages = olderMessages(); return s }, method: "GET", path: view.ChannelURL("acme", domain.ID{1}) + "?before=40", cookie: true},
		{name: "channel invalid name", route: "POST /organizations/{slug}/channels", services: withChannelError(channel.ErrInvalidName), method: "POST", path: "/organizations/acme/channels", cookie: true, form: url.Values{"name": {""}}, status: http.StatusUnprocessableEntity},
		{name: "channel duplicate name", route: "POST /organizations/{slug}/channels", services: withChannelError(channel.ErrNameTaken), method: "POST", path: "/organizations/acme/channels", cookie: true, form: url.Values{"name": {"雑談"}}, status: http.StatusUnprocessableEntity},
	}
	for _, body := range []string{"", strings.Repeat("界", 4001), "bad\u202e"} {
		for _, hx := range []bool{false, true} {
			cases = append(cases, markupCase{name: fmt.Sprintf("composer invalid %d/htmx=%t", len(body), hx), route: "POST /organizations/{slug}/channels/{channelID}", services: signedIn(oneOrganisation{}), method: "POST", path: view.ChannelURL("acme", domain.ID{1}), cookie: true, form: url.Values{"body": {body}}, status: 422, alerts: 1, htmx: hx})
		}
	}
	cases = append(cases, markupCase{name: "composer posted", route: "POST /organizations/{slug}/channels/{channelID}", services: func() Services { s := signedIn(oneOrganisation{})(); s.Messages = populatedMessages(); return s }, method: "POST", path: view.ChannelURL("acme", domain.ID{1}), cookie: true, form: url.Values{"body": {"sent"}}, htmx: true})
	// Routes that answer with a redirect, an empty status or an event
	// stream, never a page.
	for _, method := range []string{"GET", "POST"} {
		status, alerts := 200, 0
		if method == "POST" {
			status, alerts = 422, 1
		}
		cases = append(cases, markupCase{name: "topic " + method, route: method + " /organizations/{slug}/channels/{channelID}/topics/{topicID}", services: func() Services { s := signedIn(oneOrganisation{})(); s.Messages = olderMessages(); return s }, method: method, path: view.ConversationURL("acme", domain.ID{1}, &domain.ID{2}), cookie: true, form: url.Values{"body": {""}}, status: status, alerts: alerts})
	}
	cases = append(cases, markupCase{name: "branch invalid", route: "POST /organizations/{slug}/channels/{channelID}/branch", services: signedIn(oneOrganisation{}), method: "POST", path: view.ChannelURL("acme", domain.ID{1}) + "/branch", cookie: true, status: 422, alerts: 1})
	noPage := []string{"POST /signout", "GET /organizations/{slug}/{$}", "GET /organizations/{slug}/channels/{channelID}/events", "GET /organizations/{slug}/channels/{channelID}/topics/{topicID}/events"}

	_, patterns, err := newHandler("", catalogues, withSignUp(true, nil)())
	if err != nil {
		t.Fatal(err)
	}
	_, withSetupPatterns, err := newHandler("", catalogues, withSetup(nil)())
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range append(patterns, withSetupPatterns...) {
		covered := slices.Contains(noPage, pattern) || slices.ContainsFunc(cases, func(c markupCase) bool { return c.route == pattern })
		if !covered {
			t.Errorf("HTML route %q has no rendered case in TestPagesMarkup and is not listed as rendering no page", pattern)
		}
	}

	// Representative exact titles; the channel's name is user input.
	titles := map[string]string{
		"home signed out/en": "Home · ribbitto",
		"home signed out/ja": "ホーム · ribbitto",
		"sign-in/en":         "Sign in · ribbitto",
		"sign-in/ja":         "サインイン · ribbitto",
		"setup/en":           "Set up · ribbitto",
		"setup/ja":           "初期設定 · ribbitto",
		"channel/en":         "雑談 <script>alert(1)</script> · Acme Corporation · ribbitto",
		"channel/ja":         "雑談 <script>alert(1)</script> · Acme Corporation · ribbitto",
	}
	for _, c := range cases {
		for _, lang := range []string{"en", "ja"} {
			t.Run(c.name+"/"+lang, func(t *testing.T) {
				handler, _, err := newHandler("", catalogues, c.services())
				if err != nil {
					t.Fatal(err)
				}
				var w *httptest.ResponseRecorder
				for range max(c.repeat, 1) {
					r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.form.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("Accept-Language", lang)
					if c.htmx {
						r.Header.Set("HX-Request", "true")
					}
					if c.cookie {
						r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					}
					w = httptest.NewRecorder()
					handler.ServeHTTP(w, r)
				}
				want := c.status
				if want == 0 {
					want = http.StatusOK
				}
				if w.Code != want || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
					t.Fatalf("status %d (want %d), content type %q", w.Code, want, w.Header().Get("Content-Type"))
				}
				raw := w.Body.String()
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				fragment := c.htmx && c.route == "POST /organizations/{slug}/channels/{channelID}"
				for _, problem := range checkMarkup(doc, !fragment) {
					t.Error(problem)
				}
				if c.name == "channel" || c.name == "channel with messages" {
					items := find(doc, atom.Ol)
					if items == nil || attr(items, "id") != "message-items" {
						t.Fatal("channel must always render #message-items as an ordered list")
					}
					count := 0
					for child := items.FirstChild; child != nil; child = child.NextSibling {
						if child.DataAtom != atom.Li {
							t.Fatal("message list contains a non-item node that prevents :empty matching")
						}
						count++
					}
					wantCount := 0
					if c.name == "channel with messages" {
						wantCount = 2
					}
					if count != wantCount {
						t.Errorf("message items = %d, want %d", count, wantCount)
					}
				}
				if find(doc, atom.Ol) != nil {
					statusCount := 0
					for n := range doc.Descendants() {
						if _, ok := attrOK(n, "data-announcement"); ok {
							t.Error("history must not carry announcement text")
						}
						if attr(n, "role") == "status" {
							statusCount++
							if attr(n, "id") != "message-status" || attr(n, "aria-live") != "polite" || attr(n, "aria-relevant") != "additions" || attr(n, "aria-atomic") != "false" || text(n) != "" {
								t.Error("live status must start empty and polite")
							}
						}
						if attr(n, "id") == "message-items" || attr(n, "id") == "load-older" {
							for p := n; p != nil; p = p.Parent {
								if attr(p, "aria-live") != "" || slices.Contains([]string{"status", "log", "alert"}, attr(p, "role")) {
									t.Error("history must never be inside a live region")
								}
							}
						}
					}
					if statusCount != 1 {
						t.Error("channel must have exactly one live status region")
					}
				}
				checkHTMXRequests(t, handler, doc, lang, c.cookie)
				for _, problem := range checkFallbackURLs(doc) {
					t.Error(problem)
				}
				for n := range doc.Descendants() {
					var assetURL string
					switch n.DataAtom {
					case atom.Script:
						assetURL = attr(n, "src")
					case atom.Link:
						if slices.Contains(strings.Fields(strings.ToLower(attr(n, "rel"))), "stylesheet") {
							assetURL = attr(n, "href")
						}
					}
					if !strings.HasPrefix(assetURL, "/static/") {
						continue
					}
					u, err := url.Parse(assetURL)
					if err != nil {
						t.Errorf("invalid asset URL %q: %v", assetURL, err)
						continue
					}
					// Match the server: the stylesheet hash is a query, not
					// part of the path looked up in the embedded filesystem.
					info, err := fs.Stat(static.FS(), strings.TrimPrefix(u.Path, "/static/"))
					if err != nil {
						t.Errorf("asset %q: %v", assetURL, err)
					} else if info.IsDir() {
						t.Errorf("asset %q is a directory, want a file", assetURL)
					}
				}
				if want, ok := titles[c.name+"/"+lang]; ok {
					title := find(doc, atom.Title)
					if title == nil {
						t.Fatal("missing <title>")
					}
					if got := text(title); got != want {
						t.Errorf("title %q, want %q", got, want)
					}
					// <title> is raw text to the parser, so only the bytes show
					// that a name like "</title>" could not end it early.
					if !strings.Contains(raw, "<title>"+html.EscapeString(want)+"</title>") {
						t.Errorf("title not escaped as %q", html.EscapeString(want))
					}
				}
				if c.path == "/setup" || c.path == "/signup" {
					r := httptest.NewRequest(c.method, c.path, nil)
					r.Header.Set("Accept-Language", lang)
					catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
						checkTextFields(t, r.Context(), doc, c)
					})).ServeHTTP(httptest.NewRecorder(), r)
				}
				// Every invalid field's error is rendered, so each is checked too.
				if c.alerts > 0 {
					alerts := 0
					for n := range doc.Descendants() {
						if attr(n, "role") == "alert" {
							alerts++
						}
					}
					if alerts != c.alerts {
						t.Errorf("%d field errors rendered, want %d", alerts, c.alerts)
					}
				}
			})
		}
	}
}

// Follow this field's reference so other fields' alerts cannot satisfy the check.
func checkFieldError(t *testing.T, doc, field *html.Node) *html.Node {
	t.Helper()
	id := attr(field, "aria-describedby")
	if id != "" {
		for n := range doc.Descendants() {
			if attr(n, "id") == id {
				if attr(n, "role") != "alert" {
					t.Errorf("%s: description %q is not an alert", attr(field, "name"), id)
					return nil
				}
				return n
			}
		}
	}
	t.Errorf("%s: missing associated field error %q", attr(field, "name"), id)
	return nil
}

// Check the explicit naming and description sources used by these forms;
// this is not a general implementation of accessible-name computation.
func checkTextFields(t *testing.T, ctx context.Context, doc *html.Node, c markupCase) {
	t.Helper()
	fields := []string{"display_name", "handle", "email", "password"}
	if c.path == "/setup" {
		fields = append([]string{"token", "organization_name", "slug"}, fields...)
	}
	ids := map[string]*html.Node{}
	inputs := map[string]*html.Node{}
	labels := map[string][]*html.Node{}
	for n := range doc.Descendants() {
		if id := attr(n, "id"); id != "" {
			if ids[id] != nil {
				t.Errorf("duplicate id %q", id)
			}
			ids[id] = n
		}
		if n.DataAtom == atom.Input && attr(n, "type") != "hidden" {
			name := attr(n, "name")
			if inputs[name] != nil {
				t.Errorf("duplicate input %q", name)
			}
			inputs[name] = n
		}
		if n.DataAtom == atom.Label {
			labels[attr(n, "for")] = append(labels[attr(n, "for")], n)
		}
	}
	if len(inputs) != len(fields) {
		t.Errorf("%d inputs, want %d", len(inputs), len(fields))
	}
	for _, name := range fields {
		input := inputs[name]
		if input == nil {
			t.Errorf("missing input %q", name)
			continue
		}
		label := labels[attr(input, "id")]
		wantLabel := i18n.T(ctx, "setup."+name)
		if attr(input, "id") == "" || len(label) != 1 || strings.TrimSpace(text(label[0])) != wantLabel {
			t.Errorf("%s: want one explicit label containing only %q", name, wantLabel)
		}
		for _, key := range []string{"aria-label", "aria-labelledby"} {
			if _, ok := attrOK(input, key); ok {
				t.Errorf("%s: %s overrides the field label", name, key)
			}
		}
		for p := input.Parent; p != nil; p = p.Parent {
			if p.DataAtom == atom.Label {
				t.Errorf("%s: label must contain only the field name", name)
			}
		}
		if slices.Contains(c.invalidFields, name) {
			if attr(input, "aria-invalid") != "true" {
				t.Errorf("%s: missing invalid state", name)
			}
			description := checkFieldError(t, doc, input)
			if description == nil || strings.TrimSpace(text(description)) != i18n.T(ctx, "setup.error."+name) {
				t.Errorf("%s: description must reference its submit error", name)
			} else {
				for p := description.Parent; p != nil; p = p.Parent {
					if p.DataAtom == atom.Label {
						t.Errorf("%s: error is inside a label", name)
					}
				}
			}
		} else {
			for _, key := range []string{"aria-invalid", "aria-describedby"} {
				if _, ok := attrOK(input, key); ok {
					t.Errorf("%s: valid input has %s", name, key)
				}
			}
		}
		wantValue := c.form.Get(name)
		if name == "password" || name == "token" {
			wantValue = ""
		}
		if attr(input, "value") != wantValue {
			t.Errorf("%s: value was not retained or a secret was echoed", name)
		}
	}
}

// Shared components rendered on their own get the fragment rules. Add fragments
// rendered by handlers or SSE (M3) to this list when they exist.
func TestComponentsMarkup(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []struct {
		name string
		view templ.Component
	}{
		{"MessageItem", view.MessageItem(view.Message{
			ID: domain.ID{0xab, 0xcd}, DisplayName: "مريم", Handle: "author",
			CreatedAt: time.Date(2026, 9, 29, 21, 0, 0, 123456789, time.FixedZone("JST", 9*60*60)),
			Body:      "<script>bad()</script>\nمرحبا",
		})},
		{"LiveMessageItem", view.LiveMessageItem(view.Message{
			ID: domain.ID{0xab, 0xcd}, DisplayName: "مريم", Handle: "author",
			CreatedAt: time.Date(2026, 9, 29, 21, 0, 0, 123456789, time.FixedZone("JST", 9*60*60)),
			Body:      "<script>bad()</script>\nمرحبا",
		})},
		{"SignOutButton", view.SignOutButton()},
		{"MemberName", view.MemberName("مريم", "author")},
		{"MemberName blank fallback", view.MemberName("\u3164", "legacy")},
	} {
		for _, lang := range []string{"en", "ja"} {
			t.Run(component.name+"/"+lang, func(t *testing.T) {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set("Accept-Language", lang)
				catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					var b strings.Builder
					if err := component.view.Render(r.Context(), &b); err != nil {
						t.Fatal(err)
					}
					doc, err := html.Parse(strings.NewReader(b.String()))
					if err != nil {
						t.Fatal(err)
					}
					if component.name == "MessageItem" || component.name == "LiveMessageItem" {
						item := find(doc, atom.Li)
						if item == nil || attr(item, "id") != "message-abcd0000000000000000000000000000" {
							t.Fatal("standalone message lost its list item or stable DOM id")
						}
						wantAnnouncement := map[string]string{"en": "New message", "ja": "新しいメッセージ"}[lang] + " @author: <script>bad()</script>\nمرحبا"
						if component.name == "MessageItem" {
							if _, ok := attrOK(item, "data-announcement"); ok {
								t.Error("history item must not carry announcement text")
							}
						} else if attr(item, "data-announcement") != wantAnnouncement {
							t.Error("announcement must contain localized, escaped plain text")
						}
						stamp, body := find(item, atom.Time), find(item, atom.P)
						if stamp == nil || body == nil {
							t.Fatal("standalone message lost its timestamp or body")
						}
						if _, ok := attrOK(stamp, "data-local-time"); !ok {
							t.Error("timestamp lost its local-time hook")
						}
						if attr(body, "dir") != "auto" || text(body) != "<script>bad()</script>\nمرحبا" || find(item, atom.Script) != nil {
							t.Error("standalone message body lost its direction, plain text or line breaks")
						}
					}
					for _, problem := range checkMarkup(doc, false) {
						t.Error(problem)
					}
				})).ServeHTTP(httptest.NewRecorder(), r)
			})
		}
	}
}
