package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// A message's actions menu (#1159) opens below its button at desktop width
// and as a bottom sheet on a phone. "Copy link" copies the absolute message
// URL, shows and announces "Link copied" (the status keeping ten entries),
// and forgets it when the menu closes, also when the copy settles after
// that; when copying fails the link opens instead.
func testMessageActionsBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	id := kernel.ID{9}
	conversation := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:  view.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: "general"},
		Messages: []view.Message{{ID: id, TopicID: kernel.ID{2}, Handle: "author", Body: "hello", URL: view.MessageURL("acme", id)}}}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.Handle("GET /conversation", templ.Handler(view.ChannelScreen("/static/css/app.css", conversation)))
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	if err := (proto.BrowserGrantPermissions{Origin: server.URL, Permissions: []proto.BrowserPermissionType{proto.BrowserPermissionTypeClipboardReadWrite, proto.BrowserPermissionTypeClipboardSanitizedWrite}}).Call(page); err != nil {
		t.Fatal(err)
	}
	menu, button := fmt.Sprintf("#message-actions-%x", id), fmt.Sprintf(`[popovertarget="message-actions-%x"]`, id)
	for _, size := range [][2]int{{1280, 800}, {390, 844}} {
		what := fmt.Sprintf("width %d", size[0])
		page.MustActivate().MustSetViewport(size[0], size[1], 1, size[0] < 768)
		page.MustNavigate(server.URL + "/conversation").MustWaitLoad()
		page.MustElement(button).MustClick()
		// Desktop: below the button, ending at its end. Phone: a sheet on the bottom edge.
		placed := page.MustEval(`(menu, button, phone) => {
			const m = document.querySelector(menu), b = document.querySelector(button).getBoundingClientRect(), r = m.getBoundingClientRect();
			if (!m.matches(':popover-open')) return false;
			return phone ? Math.abs(r.bottom - innerHeight) < 1 && r.left === 0
				: r.top >= b.bottom - 1 && Math.abs(r.right - b.right) < 1;
		}`, menu, button, size[0] < 768).Bool()
		if !placed {
			t.Fatalf("%s: the menu is not open where it belongs", what)
		}
		page.MustElement(menu + " a").MustClick()
		page.MustWait(`menu => document.querySelector(menu + ' a').hasAttribute('data-copied')`, menu)
		got := page.MustEval(`async menu => [await navigator.clipboard.readText(), location.pathname,
			document.querySelector(menu + ' a').innerText, document.getElementById('message-status').textContent].join('|')`, menu).Str()
		// Served without the catalogues, the label is its message ID.
		if want := server.URL + view.MessageURL("acme", id) + "|/conversation|message.link_copied|message.link_copied\n"; got != want {
			t.Fatalf("%s: clipboard, path, label, status = %q; want %q", what, got, want)
		}
		page.MustEval(`menu => document.querySelector(menu).hidePopover()`, menu)
		if page.MustEval(`menu => document.querySelector(menu + ' a').hasAttribute('data-copied')`, menu).Bool() {
			t.Fatalf("%s: a closed menu keeps the confirmation", what)
		}
	}
	// Twelve copies leave ten announcements, without a live message to prune them.
	if n := page.MustEval(`async menu => { const m = document.querySelector(menu);
		for (let i = 0; i < 11; i++) { m.showPopover(); m.querySelector('a').click(); await new Promise(r => setTimeout(r, 50)); m.hidePopover(); }
		return document.getElementById('message-status').childNodes.length; }`, menu).Int(); n != 10 {
		t.Fatalf("the status region holds %d announcements, want 10", n)
	}
	// A copy that settles after the menu closed leaves it saying "Copy link".
	if page.MustEval(`async menu => { const m = document.querySelector(menu), a = m.querySelector('a'), write = navigator.clipboard.writeText.bind(navigator.clipboard);
		navigator.clipboard.writeText = text => new Promise(r => setTimeout(r, 100)).then(() => write(text));
		m.hidePopover(); m.showPopover(); a.click(); m.hidePopover(); m.showPopover();
		await new Promise(r => setTimeout(r, 300)); const copied = a.hasAttribute('data-copied'); navigator.clipboard.writeText = write; m.hidePopover(); return copied; }`, menu).Bool() {
		t.Fatal("a copy that settled after the menu closed marked it copied")
	}
	// A failed copy opens the link, as without JavaScript.
	page.MustEval(`button => { navigator.clipboard.writeText = () => Promise.reject(new Error('denied')); document.querySelector(button).click(); }`, button)
	page.MustElement(menu + " a").MustClick()
	page.MustWait(`path => location.pathname === path`, view.MessageURL("acme", id))
}
