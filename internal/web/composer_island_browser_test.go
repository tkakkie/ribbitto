package web

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// #1152's prototype: one browser case per gate of the composer island. Each
// case states today's composer behaviour, which the island must keep.

const islandScript = "/static/islands/composer-v1.js"

// islandProbe records, from document start, what the gates count: submits,
// state and signal-form swaps, the island's lifecycle marks and CSP
// violations.
const islandProbe = `(() => {
 window.islandProbe = {submits: 0, states: 0, signals: 0, violations: []};
 document.addEventListener('submit', e => { if (e.target.id === 'message-composer') window.islandProbe.submits++; }, true);
 document.addEventListener('htmx:afterSettle', e => { if (e.target.id === 'message-composer-state') window.islandProbe.states++; });
 document.addEventListener('htmx:oobAfterSwap', e => { if (e.target.id === 'typing-form') window.islandProbe.signals++; });
 document.addEventListener('securitypolicyviolation', e => window.islandProbe.violations.push(e.violatedDirective + ' ' + e.blockedURI));
 window.islandMarks = () => ({mount: performance.getEntriesByName('island:mount').length, destroy: performance.getEntriesByName('island:destroy').length});
})()`

type islandServer struct {
	page    *rod.Page
	posts   atomic.Int32
	signals atomic.Int32
	url     string
}

// newIslandServer serves composerHandler's feed or topic with the real
// handler (its CSP included), counting posts and typing signals.
func newIslandServer(t *testing.T, browser *rod.Browser, topic bool) *islandServer {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	handler, _, _, place := composerHandler(t, topic)
	s := &islandServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		if r.Method == "POST" {
			if strings.HasSuffix(r.URL.Path, "/typing") {
				s.signals.Add(1)
			} else {
				s.posts.Add(1)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	s.url = server.URL + view.ConversationURL("acme", place.Channel, nil)
	if topic {
		s.url = server.URL + view.ConversationURL("acme", place.Channel, &place.Topic)
	}
	s.page = browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	t.Cleanup(func() { s.page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
	s.page.MustEvalOnNewDocument(islandProbe)
	return s
}

func (s *islandServer) open() {
	s.page.MustNavigate(s.url).MustWaitLoad()
	s.page.MustElement(`#message-body[data-island="mounted"]`)
}

// send presses Enter in the textarea and waits until the answer settled.
func (s *islandServer) send(text string) {
	states := s.page.MustEval(`() => window.islandProbe.states`).Int()
	s.page.MustEval(`text => { const b = document.getElementById('message-body'); b.value = text; b.focus(); }`, text)
	s.page.Keyboard.MustType(input.Enter)
	s.page.MustWait(`n => window.islandProbe.states > n && !document.querySelector('#message-composer button').disabled`, states)
}

// oneIsland reports what breaks "exactly one working island": a second or
// replaced textarea, lost focus, unequal generations or unbalanced marks.
const oneIsland = `(mounts) => {
 const bodies = document.querySelectorAll('textarea[name="body"]');
 const body = document.getElementById('message-body');
 if (bodies.length !== 1 || body.dataset.island !== 'mounted') return bodies.length + ' textareas, island ' + body.dataset.island;
 if (document.activeElement !== body) return 'focus is not in the textarea';
 const post = document.querySelector('#message-composer-state input[name="composer"]').value;
 const signal = document.querySelector('#typing-form input[name="composer"]').value;
 if (!post || post !== signal) return 'generations differ: ' + post + ' ' + signal;
 const marks = window.islandMarks();
 if (marks.mount !== mounts || marks.mount !== marks.destroy + 1) return 'marks ' + JSON.stringify(marks);
 return '';
}`

func testComposerIslandBrowser(t *testing.T, browser *rod.Browser) {
	t.Run("swaps", func(t *testing.T) { testIslandSwaps(t, browser) })
	t.Run("ime", func(t *testing.T) { testIslandIME(t, browser) })
	t.Run("before-mount", func(t *testing.T) { testIslandBeforeMount(t, browser) })
	t.Run("csp", func(t *testing.T) { testIslandCSP(t, browser) })
	t.Run("markup", func(t *testing.T) { testIslandMarkup(t, browser) })
}

// Swap survival: success and 422 replace #message-composer-state and, out of
// band, #typing-form, never the island; stream and history insertions leave
// it alone; and if something did replace the whole form, the old island is
// destroyed and exactly one new one mounts.
func testIslandSwaps(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		t.Run(map[bool]string{false: "feed", true: "topic"}[topic], func(t *testing.T) {
			s := newIslandServer(t, browser, topic)
			s.open()
			p := s.page
			p.MustEval(`() => { document.getElementById('message-body').islandTag = 1;
 for (const id of ['message-composer-state', 'typing-form']) document.getElementById(id).islandTag = 1; }`)
			replaced := `() => { const ids = ['message-composer-state', 'typing-form'].filter(id => document.getElementById(id).islandTag);
 for (const id of ['message-composer-state', 'typing-form']) document.getElementById(id).islandTag = 1;
 return document.getElementById('message-body').islandTag === 1 && ids.length === 0; }`
			for _, text := range []string{"hello", "  ", "again"} {
				signals := p.MustEval(`() => window.islandProbe.signals`).Int()
				s.send(text)
				if !p.MustEval(replaced).Bool() || p.MustEval(`() => window.islandProbe.signals`).Int() != signals+1 {
					t.Fatalf("after %q the state and signal form were not both replaced, or the textarea was", text)
				}
				if problem := p.MustEval(oneIsland, 1).Str(); problem != "" {
					t.Fatalf("after %q: %s", text, problem)
				}
			}
			if submits, posts := p.MustEval(`() => window.islandProbe.submits`).Int(), s.posts.Load(); submits != 3 || posts != 3 {
				t.Fatalf("%d submits and %d posts for three Enters", submits, posts)
			}
			// Stream and Load older insertions are processed by htmx and fire
			// htmx:load on new items, which must not mount a second island.
			p.MustEval(`() => htmx.swap('#message-items', '<li id="message-00000000000000000000000000000099">x</li>', {swapStyle: 'beforeend'})`)
			p.MustEval(`() => htmx.swap('#message-items', '<li id="message-00000000000000000000000000000098">y</li>', {swapStyle: 'afterbegin'})`)
			if problem := p.MustEval(oneIsland, 1).Str(); problem != "" || p.MustEval(`() => document.getElementById('message-body').islandTag`).Int() != 1 {
				t.Fatalf("after insertions: %s", problem)
			}
			// No template swaps the form today; a swap that did must leave one
			// island, and the old one's state must go with it.
			p.MustEval(`async (url) => {
 const html = await (await fetch(url)).text();
 const doc = new DOMParser().parseFromString(html, 'text/html');
 for (const id of ['message-composer', 'typing-form']) htmx.swap('#' + id, doc.getElementById(id).outerHTML, {swapStyle: 'outerHTML'});
}`, s.url)
			p.MustWait(`() => document.getElementById('message-body').dataset.island === 'mounted' && window.islandMarks().destroy === 1`)
			p.MustEval(`() => document.getElementById('message-body').focus()`)
			if problem := p.MustEval(oneIsland, 2).Str(); problem != "" {
				t.Fatalf("after replacing the form: %s", problem)
			}
			// One Enter is one submit: no listener of the old island remains.
			s.send("after replacement")
			if submits := p.MustEval(`() => window.islandProbe.submits`).Int(); submits != 4 || s.posts.Load() != 4 {
				t.Fatalf("%d submits and %d posts after the replacement, want 4", submits, s.posts.Load())
			}
			if got := p.MustEval(`() => document.getElementById('message-body').value`).Str(); got != "" {
				t.Fatalf("the replaced island did not remove the sent text: %q", got)
			}
		})
	}
}

// IME: Enter inside a composition, with keyCode 229, or within 100 ms of
// compositionend sends nothing; a later Enter sends; composition updates send
// no typing signal.
func testIslandIME(t *testing.T, browser *rod.Browser) {
	s := newIslandServer(t, browser, false)
	s.open()
	p := s.page
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	enter := func(code int) {
		t.Helper()
		down := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: code}
		must(down.Call(p))
		up := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyUp, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: code}
		must(up.Call(p))
	}
	p.MustEval(`() => document.getElementById('message-body').focus()`)
	must(proto.InputImeSetComposition{Text: "に", SelectionStart: 1, SelectionEnd: 1}.Call(p))
	must(proto.InputImeSetComposition{Text: "にほ", SelectionStart: 2, SelectionEnd: 2}.Call(p))
	enter(13)
	if s.signals.Load() != 0 {
		t.Fatal("a composition update sent a typing signal")
	}
	// The commit ends the composition and sends one signal.
	must(proto.InputInsertText{Text: "日本"}.Call(p))
	// An Enter right after compositionend belongs to the composition; events
	// dispatched together keep it within 100 ms whatever the machine's load.
	p.MustEval(`() => { const b = document.getElementById('message-body');
 b.dispatchEvent(new CompositionEvent('compositionend', {bubbles: true}));
 b.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true})); }`)
	time.Sleep(150 * time.Millisecond)
	enter(229)
	if submits := p.MustEval(`() => window.islandProbe.submits`).Int(); submits != 0 || s.posts.Load() != 0 {
		t.Fatalf("%d submits during or right after the composition", submits)
	}
	if got := p.MustEval(`() => document.getElementById('message-body').value`).Str(); got != "日本" {
		t.Fatalf("the composition left %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.signals.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.signals.Load() == 0 {
		t.Fatal("no typing signal after the commit")
	}
	states := p.MustEval(`() => window.islandProbe.states`).Int()
	enter(13)
	p.MustWait(`n => window.islandProbe.states > n`, states)
	if submits := p.MustEval(`() => window.islandProbe.submits`).Int(); submits != 1 || s.posts.Load() != 1 {
		t.Fatalf("%d submits for a later Enter, want 1", submits)
	}
}

// No-JavaScript fallback, while the island is still loading: the
// server-rendered form is usable, and what was typed into it (text, caret,
// focus) stays when the island takes over.
func testIslandBeforeMount(t *testing.T, browser *rod.Browser) {
	s := newIslandServer(t, browser, false)
	p := s.page
	release := make(chan struct{})
	router := p.HijackRequests()
	router.MustAdd("*"+islandScript, func(h *rod.Hijack) {
		<-release
		h.MustLoadResponse()
	})
	go router.Run()
	t.Cleanup(func() { _ = router.Stop() })
	wait := p.WaitNavigation(proto.PageLifecycleEventNameDOMContentLoaded)
	go func() { _ = p.Navigate(s.url) }()
	el := p.MustElement("#message-body")
	if island := el.MustAttribute("data-island"); island != nil {
		close(release)
		t.Fatal("the island mounted before its script loaded")
	}
	el.MustInput("typed early")
	p.MustEval(`() => document.getElementById('message-body').setSelectionRange(2, 5)`)
	close(release)
	wait()
	p.MustElement(`#message-body[data-island="mounted"]`)
	if got := p.MustEval(`() => { const b = document.getElementById('message-body');
 return JSON.stringify([b.value, b.selectionStart, b.selectionEnd, document.activeElement === b]); }`).Str(); got != `["typed early",2,5,true]` {
		t.Fatalf("after mounting: %s", got)
	}
	states := p.MustEval(`() => window.islandProbe.states`).Int()
	p.Keyboard.MustType(input.Enter)
	p.MustWait(`n => window.islandProbe.states > n`, states)
	if s.posts.Load() != 1 || p.MustEval(`() => document.getElementById('message-body').value`).Str() != "" {
		t.Fatal("the early text was not sent by the island")
	}
}

// CSP: under the real policy, no violation; the island is one external,
// nonce-loaded file with no chunk or dynamic import; it adds no <style> and
// no style attribute.
func testIslandCSP(t *testing.T, browser *rod.Browser) {
	source, err := fs.ReadFile(static.FS(), strings.TrimPrefix(islandScript, "/static/"))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`\bimport\s*\(|\bimport\s*["'{*]|<style|\.style\b|setAttribute\(\s*["']style`).Match(source) {
		t.Fatal("the island imports at runtime or writes styles")
	}
	for _, topic := range []bool{false, true} {
		s := newIslandServer(t, browser, topic)
		s.open()
		s.send("hello")
		s.send("  ")
		got := s.page.MustEval(`() => {
 const scripts = [...document.scripts].filter(s => s.src.includes('/static/islands/'));
 const loaded = performance.getEntriesByType('resource').filter(r => r.name.includes('/static/islands/'));
 return {violations: window.islandProbe.violations, scripts: scripts.length, nonce: scripts.every(s => s.nonce !== ''),
  loaded: loaded.length, styles: document.querySelectorAll('style').length, styled: document.querySelectorAll('[style]').length};
}`)
		if v := got.Get("violations").Arr(); len(v) != 0 {
			t.Fatalf("CSP violations: %v", v)
		}
		if got.Get("scripts").Int() != 1 || !got.Get("nonce").Bool() || got.Get("loaded").Int() != 1 {
			t.Fatalf("island files: %v", got)
		}
		if got.Get("styles").Int() != 0 || got.Get("styled").Int() != 0 {
			t.Fatalf("styles injected: %v", got)
		}
	}
}

// Markup and text: the label, aria-describedby, aria-invalid, the alert and
// every string come from the server, in each language; the island's textarea
// keeps the server textarea's attributes, and its file holds no catalogue text.
func testIslandMarkup(t *testing.T, browser *rod.Browser) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	source, err := fs.ReadFile(static.FS(), strings.TrimPrefix(islandScript, "/static/"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"message.body", "message.help", "message.send", "message.sending", "message.error.body", "message.error.repeat"}
	for _, lang := range []string{"en", "ja"} {
		ctx := languageContext(catalogues, lang)
		for _, id := range ids {
			if text := i18n.T(ctx, id); bytes.Contains(source, []byte(text)) {
				t.Errorf("the island holds %s's %s text %q", lang, id, text)
			}
		}
		s := newIslandServer(t, browser, false)
		s.page.MustSetCookies(&proto.NetworkCookieParam{Name: "lang", Value: lang, URL: s.url})
		t.Cleanup(func() { deleteLangCookie(t, s.page, s.url) })
		s.open()
		server := s.page.MustEval(`async (url) => {
 const doc = new DOMParser().parseFromString(await (await fetch(url)).text(), 'text/html');
 const b = doc.getElementById('message-body');
 return [...b.attributes].map(a => a.name + '=' + a.value).sort().join(' ');
}`, s.url)
		mounted := s.page.MustEval(`() => [...document.getElementById('message-body').attributes]
 .filter(a => a.name !== 'data-island').map(a => a.name + '=' + a.value).sort().join(' ')`)
		if mounted.Str() != server.Str() {
			t.Fatalf("%s: island textarea %q, server textarea %q", lang, mounted.Str(), server.Str())
		}
		check := func(invalid bool) {
			t.Helper()
			got := s.page.MustEval(`() => { const b = document.getElementById('message-body'), help = document.getElementById('message-help');
 return {label: document.querySelector('label[for="message-body"]').textContent, described: b.getAttribute('aria-describedby'),
  invalid: b.getAttribute('aria-invalid'), help: help.textContent, alert: help.getAttribute('role') || ''}; }`)
			want := map[string]string{"label": i18n.T(ctx, "message.body"), "described": "message-help", "invalid": "false", "help": i18n.T(ctx, "message.help"), "alert": ""}
			if invalid {
				want["invalid"], want["help"], want["alert"] = "true", i18n.T(ctx, "message.error.body"), "alert"
			}
			for k, v := range want {
				if got.Get(k).Str() != v {
					t.Fatalf("%s invalid=%t: %s is %q, want %q", lang, invalid, k, got.Get(k).Str(), v)
				}
			}
		}
		check(false)
		s.send("  ")
		check(true)
		s.send("hello")
		check(false)
	}
}

// languageContext is a request context in lang, for i18n.T.
func languageContext(c *i18n.Catalogues, lang string) context.Context {
	ctx := context.Background()
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "lang", Value: lang})
	c.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	return ctx
}
