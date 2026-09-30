package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// checkMarkup reports what breaks the rules in docs/ui.md, *Markup and
// accessibility*. Full pages get the document checks; fragments and
// components only the element checks.
// appName is the English and Japanese app.title, which alone names no page.
const appName = "ribbitto"

func checkMarkup(doc *html.Node, fullPage bool) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	labelled := map[string]bool{} // ids named by <label for>
	for n := range doc.Descendants() {
		if n.DataAtom == atom.Label && attr(n, "for") != "" {
			labelled[attr(n, "for")] = true
		}
	}
	mains, lastHeading := 0, 0
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		for _, a := range n.Attr {
			if a.Key == "style" || strings.HasPrefix(a.Key, "on") {
				add("<%s> has a %s attribute", n.Data, a.Key)
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

// The checker itself: each rule catches what it should and nothing more.
func TestCheckMarkup(t *testing.T) {
	page := func(body string) string {
		return `<!DOCTYPE html><html lang="en"><head><title>t</title></head><body>` + body + `</body></html>`
	}
	for _, tt := range []struct {
		name, markup string
		fullPage     bool
		want         string // a substring of the only problem; empty for none
	}{
		{"valid page", page(`<main><h1>a</h1><h2>b</h2><form><label>Email <input type="email" name="email"></label><label for="p">Password</label><input id="p" type="password" name="p"><input type="hidden" name="t"><button type="submit">Go</button></form><img src="x" alt=""></main>`), true, ""},
		{"no lang", `<!DOCTYPE html><html><head><title>t</title></head><body><main></main></body></html>`, true, "without lang"},
		{"no title", `<!DOCTYPE html><html lang="en"><body><main></main></body></html>`, true, "<title>"},
		{"blank title", `<!DOCTYPE html><html lang="en"><head><title> </title></head><body><main></main></body></html>`, true, "<title>"},
		{"title is only the app name", `<!DOCTYPE html><html lang="en"><head><title>ribbitto</title></head><body><main></main></body></html>`, true, "<title>"},
		{"two mains", page(`<main></main><main></main>`), true, "2 <main>"},
		{"skipped heading", page(`<main><h1>a</h1><h3>b</h3></main>`), true, "skips a heading level"},
		{"unlabelled input", page(`<main><input type="text" name="q"></main>`), true, "without a label"},
		{"button without type", page(`<main><button>Go</button></main>`), true, "without a type"},
		{"button without name", page(`<main><button type="button"> </button></main>`), true, "accessible name"},
		{"img without alt", page(`<main><img src="x"></main>`), true, "without alt"},
		{"style attribute", page(`<main style="color:red"></main>`), true, "style attribute"},
		{"inline handler", page(`<main><a href="/" onclick="x()">a</a></main>`), true, "onclick attribute"},
		{"simulated button", page(`<main><div role="button">Go</div></main>`), true, "simulates a button"},
		{"link without href as a button", page(`<main><a role="button" hx-post="/x">Go</a></main>`), true, "simulates a button"},
		{"icon button named only by hidden text", page(`<main><button type="button"><span aria-hidden="true">×</span></button></main>`), true, "accessible name"},
		{"blank aria-label", page(`<main><button type="button" aria-label=" "></button></main>`), true, "accessible name"},
		{"icon button with a name", page(`<main><button type="button" aria-label="Close"><span aria-hidden="true">×</span></button></main>`), true, ""},
		// An empty for names no control, whether the label wraps it or not.
		{"empty for beside the input", page(`<main><label for="">Email</label><input type="text" name="email"></main>`), true, "without a label"},
		{"empty for on a wrapping label", page(`<main><label for="">Email <input type="text" name="email"></label></main>`), true, "without a label"},
		{"wrapping label pointing elsewhere", page(`<main><label for="missing">Email <input id="email" type="email" name="email"></label></main>`), true, "without a label"},
		{"second control in one label", page(`<main><label>Name <input type="text" name="a"><input type="text" name="b"></label></main>`), true, "name=\"b\""},
		{"stray tabindex", page(`<main><div tabindex="0">x</div></main>`), true, "tabindex"},
		{"focus target", page(`<main><h1 tabindex="-1">a</h1><div role="region" aria-label="Messages" tabindex="0">x</div></main>`), true, ""},
		{"fragment skips document checks", `<form><button type="submit">Go</button></form>`, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(tt.markup))
			if err != nil {
				t.Fatal(err)
			}
			problems := checkMarkup(doc, tt.fullPage)
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want one containing %q", problems, tt.want)
			}
		})
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
	// Routes that answer with a redirect or an empty status, never a page.
	noPage := []string{"POST /signout", "GET /organizations/{slug}/{$}"}

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
				for _, problem := range checkMarkup(doc, true) {
					t.Error(problem)
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
			description := ids[attr(input, "aria-describedby")]
			if description == nil || attr(description, "role") != "alert" || strings.TrimSpace(text(description)) != i18n.T(ctx, "setup.error."+name) {
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

// Shared components rendered on their own get the element checks.
func TestComponentsMarkup(t *testing.T) {
	var b strings.Builder
	if err := view.SignOutButton().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range checkMarkup(doc, false) {
		t.Error(problem)
	}
}
