package main

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestReadSidebarBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "read sidebar acceptance")
	f := newReadSidebarFixture(t, pgtest.New(t))
	var pages []*rod.Page
	for _, path := range []string{f.topicA, f.feed} {
		page, closePage := boundedPage(t, browser, ctx, "read page")
		defer closePage()
		u, err := url.Parse(f.reader.server.URL)
		acceptanceOK(t, err)
		for _, cookie := range f.reader.client.Jar.Cookies(u) {
			page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
		}
		page.MustEvalOnNewDocument(`window.shown = 'hidden'; window.reads = 0; window.recounts = 0; window.badID = false;
			Object.defineProperty(document, 'visibilityState', {get: () => window.shown});
			document.addEventListener('htmx:beforeRequest', e => { if (e.target.id === 'read-form') window.reads++; });
			document.addEventListener('htmx:sseMessage', e => {
				if (e.detail.type === 'sidebar') { window.recounts++; window.badID ||= e.detail.lastEventId !== ''; }
			});`)
		page.MustNavigate(f.reader.server.URL + path).MustWaitLoad()
		page.MustWait(`() => window.recounts === 1 && !document.querySelector('.htmx-settling')`)
		pages = append(pages, page)
	}
	// Before any reveal, neither hidden page has sent a read, initial recount included.
	for _, page := range pages {
		if page.MustEval(`() => window.reads`).Int() != 0 {
			t.Fatal("a hidden page read before it was revealed")
		}
	}
	inputStep(t, pages[0].MustActivate(), "typing the draft", func(p *rod.Page) { p.MustElement("#message-body").MustInput("retain draft selection") })
	pages[0].MustEval(`() => document.getElementById('message-body').setSelectionRange(2, 8)`)
	pages[1].MustActivate().MustEval(`id => document.getElementById('link-' + id).focus()`, view.ChannelDOMID(f.channel))
	for step, active := range pages {
		active.MustActivate().MustEval(`() => { window.shown = 'visible'; document.dispatchEvent(new Event('visibilitychange')); }`)
		for i, page := range pages {
			wantB := "3"
			if step == 1 {
				wantB = ""
			}
			page.MustWait(`(a, b, channel, wantB, frames) => {
				// Scoped to the row's own link: a channel row also holds its topic rows.
				const badge = id => document.getElementById('link-' + id).querySelector('[data-unread-count]:not([hidden]) > .sr-only')?.previousElementSibling?.textContent.trim() || '';
				return window.recounts >= frames && badge(a) === '' && badge(b) === wantB && badge(channel) === wantB &&
					!document.querySelector('.htmx-settling') && !document.querySelector('#read-form.htmx-request');
			}`, view.TopicDOMID(f.a), view.TopicDOMID(f.b), view.ChannelDOMID(f.channel), wantB, step+2)
			path, selected := f.feed, view.ChannelDOMID(f.channel)
			if i == 0 {
				path, selected = f.topicA, view.TopicDOMID(f.a)
			}
			_, reload := f.reader.visit(t, "GET", path, nil, 200)
			if !page.MustEval(`(markup, selected, composer) => {
				const reload = new DOMParser().parseFromString(markup, 'text/html');
				const text = node => node.textContent.replace(/\s+/g, ' ').trim();
				const stream = document.getElementById('organization-stream'), input = document.getElementById('message-body');
				return ['sidebar-channels', 'sidebar-topics'].every(id => text(document.getElementById(id)) === text(reload.getElementById(id))) &&
					document.getElementById('link-' + selected).getAttribute('aria-current') === 'page' &&
					(composer ? document.activeElement === input && input.value === 'retain draft selection' && input.selectionStart === 2 && input.selectionEnd === 8 : document.activeElement.id === 'link-' + selected) &&
					stream.dataset.eventCursor === '6' && (!stream.dataset.appliedCursor || stream.dataset.appliedCursor === '6') &&
					new URL(stream.getAttribute('sse-connect'), location.href).searchParams.get('after') === '6' && !window.badID &&
					!document.getElementById('stream-sink').hasChildNodes();
			}`, reload, selected, i == 0).Bool() {
				t.Fatal("read sidebar lost exact counts, focus/selection or durable cursor")
			}
		}
		// Exercise the visibility trigger again, after swaps and request settlement.
		// A microtask and rendering barrier drain the application's queued flush.
		active.MustEval(`() => { document.dispatchEvent(new Event('visibilitychange')); return new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))); }`)
		for i, page := range pages {
			want := 0
			if i <= step {
				want = 1
			}
			if page.MustEval(`() => window.reads`).Int() != want {
				t.Fatal("sidebar swap caused a read-feedback loop or read a hidden sibling")
			}
		}
	}
}
