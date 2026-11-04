package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// A message's actions menu (#1159) opens below its button at desktop width
// and as a bottom sheet on a phone. "Copy link" copies the absolute message
// URL, shows and announces "Link copied" (the status keeping ten entries),
// ignores an older overlapping copy that settles after a newer one, and
// forgets it when the menu closes, also when the copy settles after
// that; when copying fails the link opens instead.
func testMessageActionsBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	id := kernel.ID{9}
	conversation := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:  view.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: "general"},
		Messages: []view.Message{{ID: id, TopicID: kernel.ID{2}, Handle: "author", Body: "hello", URL: view.MessageURL("acme", id)}}}
	otherID := kernel.ID{10}
	conversation.Messages = append(conversation.Messages, view.Message{ID: otherID, Body: "other message", URL: view.MessageURL("acme", otherID)})
	allowNavigation := make(chan struct{})
	var releaseNavigation sync.Once
	release := func() { releaseNavigation.Do(func() { close(allowNavigation) }) }
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.Handle("GET /conversation", templ.Handler(view.ChannelScreen("/static/css/app.css", conversation)))
	mux.HandleFunc("GET "+view.MessageURL("acme", id), func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-allowNavigation:
		case <-r.Context().Done():
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	defer release()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	if err := (proto.BrowserGrantPermissions{Origin: server.URL, Permissions: []proto.BrowserPermissionType{proto.BrowserPermissionTypeClipboardReadWrite, proto.BrowserPermissionTypeClipboardSanitizedWrite}}).Call(page); err != nil {
		t.Fatal(err)
	}
	menu := messageMenu(id)
	for _, size := range [][2]int{{1280, 800}, {390, 844}} {
		what := fmt.Sprintf("width %d", size[0])
		page.MustActivate().MustSetViewport(size[0], size[1], 1, size[0] < 768)
		page.MustNavigate(server.URL + "/conversation").MustWaitLoad()
		menu.open(page)
		// Desktop: below the button, ending at its end. Phone: a sheet on the bottom edge.
		placed := menu.eval(page, `({menu: m, button}, phone) => {
			const b = button.getBoundingClientRect(), r = m.getBoundingClientRect();
			if (!m.matches(':popover-open')) return false;
			return phone ? Math.abs(r.bottom - innerHeight) < 1 && r.left === 0
				: r.top >= b.bottom - 1 && Math.abs(r.right - b.right) < 1;
		}`, size[0] < 768).Bool()
		if !placed {
			t.Fatalf("%s: the menu is not open where it belongs", what)
		}
		for selector, name := range map[string]string{"button": "message.actions", "copy": "message.copy_link", "quote": "message.quote", "pin": "message.pin"} {
			tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: menu.control(page, selector).Object.ObjectID}).Call(page)
			if err != nil {
				t.Fatal(err)
			}
			if len(tree.Nodes) == 0 || tree.Nodes[0].Ignored || tree.Nodes[0].Name == nil || tree.Nodes[0].Name.Value.Str() != name {
				t.Fatalf("%s: %s lost its accessible name", what, selector)
			}
			if selector == "button" && (tree.Nodes[0].Description == nil || tree.Nodes[0].Description.Value.Str() != "hello") {
				t.Fatalf("%s: actions button lost its message description", what)
			}
		}
		menu.control(page, "copy").MustClick()
		menu.wait(page, `({copy}) => copy.hasAttribute('data-copied')`)
		got := menu.eval(page, `async ({copy}) => [await navigator.clipboard.readText(), location.pathname,
			copy.innerText, document.getElementById('message-status').textContent].join('|')`).Str()
		// Served without the catalogues, the label is its message ID.
		if want := server.URL + view.MessageURL("acme", id) + "|/conversation|message.link_copied|message.link_copied\n"; got != want {
			t.Fatalf("%s: clipboard, path, label, status = %q; want %q", what, got, want)
		}
		menu.eval(page, `({menu: m}) => m.hidePopover()`)
		if menu.eval(page, `({copy}) => copy.hasAttribute('data-copied')`).Bool() {
			t.Fatalf("%s: a closed menu keeps the confirmation", what)
		}
		other := messageMenu(otherID)
		other.open(page)
		if !other.eval(page, `({menu, copy, quote, form}, url, id) => document.querySelectorAll('[popover][id="message-actions"]').length === 1 && menu.contains(form) && copy.getAttribute('href') === url && quote.value === id && form.dataset.pinControl === id && !copy.hasAttribute('data-copied')`, view.MessageURL("acme", otherID), realtime.FormatPayloadID(otherID)).Bool() {
			t.Fatalf("%s: shared menu did not retarget all actions to the second message", what)
		}
		other.eval(page, `({menu}) => menu.hidePopover()`)
		menu.open(page)
		menu.eval(page, `({menu}) => menu.hidePopover()`)
	}
	// Twelve copies leave ten announcements, without a live message to prune them.
	if n := menu.eval(page, `async ({menu: m, copy}) => {
		for (let i = 0; i < 11; i++) { m.showPopover(); copy.click(); await new Promise(r => setTimeout(r, 50)); m.hidePopover(); }
		return document.getElementById('message-status').childNodes.length; }`).Int(); n != 10 {
		t.Fatalf("the status region holds %d announcements, want 10", n)
	}
	// A copy that settles after the menu closed leaves it saying "Copy link".
	if menu.eval(page, `async ({menu: m, copy}) => { const a = copy, write = navigator.clipboard.writeText.bind(navigator.clipboard);
		navigator.clipboard.writeText = text => new Promise(r => setTimeout(r, 100)).then(() => write(text));
		m.hidePopover(); m.showPopover(); a.click(); m.hidePopover(); m.showPopover();
		await new Promise(r => setTimeout(r, 300)); const copied = a.hasAttribute('data-copied'); navigator.clipboard.writeText = write; m.hidePopover(); return copied; }`).Bool() {
		t.Fatal("a copy that settled after the menu closed marked it copied")
	}
	// A copy that fails after the menu closed does not open the link.
	if path := menu.eval(page, `async ({menu: m, copy}) => { const write = navigator.clipboard.writeText.bind(navigator.clipboard);
		navigator.clipboard.writeText = () => new Promise((_, reject) => setTimeout(() => reject(new Error('denied')), 100));
		m.showPopover(); copy.click(); m.hidePopover();
		await new Promise(r => setTimeout(r, 300)); navigator.clipboard.writeText = write; return location.pathname; }`).Str(); path != "/conversation" {
		t.Fatalf("a copy that failed after the menu closed navigated to %s", path)
	}
	// Overlapping copies on the same link settle out of order: a late older
	// resolution or rejection neither changes the visible state, adds an
	// announcement nor navigates after the newer copy has settled.
	wantOverlap := strings.Join([]string{
		"2|false|0",
		"true|message.link_copied|1|message.link_copied|/conversation|",
		"true|message.link_copied|1|message.link_copied|/conversation|",
		"true|message.link_copied|1|message.link_copied|/conversation|",
		"false|message.copy_link|0||/conversation|" + view.MessageURL("acme", id),
	}, "\n")
	if got := menu.eval(page, `async ({menu: m, copy}) => {
		const a = copy, status = document.getElementById('message-status');
		const write = navigator.clipboard.writeText.bind(navigator.clipboard);
		const calls = [], navigations = [];
		const onNavigate = e => navigations.push(new URL(e.destination.url).pathname);
		window.navigation.addEventListener('navigate', onNavigate);
		navigator.clipboard.writeText = () => {
			let resolve, reject;
			const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
			calls.push({ resolve, reject, settled: () => promise.then(() => {}, () => {}) });
			return promise;
		};
		const reset = () => {
			m.hidePopover();
			m.showPopover();
			status.replaceChildren();
			calls.length = 0;
			navigations.length = 0;
		};
		const state = () => [a.hasAttribute('data-copied'), a.innerText, status.childNodes.length, status.textContent.trim(), location.pathname, navigations.join(',')].join('|');

		reset();
		a.click();
		a.click();
		const before = [calls.length, a.hasAttribute('data-copied'), status.childNodes.length].join('|');
		calls[1].resolve();
		await calls[1].settled();
		const afterNewerSuccess = state();
		calls[0].resolve();
		await calls[0].settled();
		const afterLateSuccess = state();

		reset();
		a.click();
		a.click();
		calls[1].resolve();
		await calls[1].settled();
		calls[0].reject(new Error('denied'));
		await calls[0].settled();
		const afterLateFailure = state();

		reset();
		a.click();
		a.click();
		calls[1].reject(new Error('denied'));
		await calls[1].settled();
		calls[0].resolve();
		await calls[0].settled();
		const afterNewerFailure = state();

		window.navigation.removeEventListener('navigate', onNavigate);
		navigator.clipboard.writeText = write;
		return [before, afterNewerSuccess, afterLateSuccess, afterLateFailure, afterNewerFailure].join('\n');
	}`).Str(); got != wantOverlap {
		t.Fatalf("overlapping copies state =\n%s\nwant\n%s", got, wantOverlap)
	}
	release()
	page.MustWait(`path => location.pathname === path`, view.MessageURL("acme", id))
	page.MustNavigate(server.URL + "/conversation").MustWaitLoad()
	// A failed copy opens the link, as without JavaScript.
	menu.eval(page, `({button}) => { navigator.clipboard.writeText = () => Promise.reject(new Error('denied')); button.click(); }`)
	menu.control(page, "copy").MustClick()
	page.MustWait(`path => location.pathname === path`, view.MessageURL("acme", id))
}
