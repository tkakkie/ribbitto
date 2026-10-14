package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestLiveSidebarBrowser(t *testing.T) {
	unavailable := func(reason string) {
		t.Helper()
		if os.Getenv("RIBBITTO_REQUIRE_BROWSER") == "1" {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	bin, found := launcher.LookPath()
	if !found {
		unavailable("no installed browser; Claude runs production sidebar acceptance locally")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	control, err := launch.Launch()
	if err != nil {
		unavailable(err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(control).MustConnect().MustIgnoreCertErrors(true)
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })
	f := newSidebarBranchFixture(t)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	u, err := url.Parse(f.reader.server.URL)
	acceptanceOK(t, err)
	for _, cookie := range f.reader.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
	}
	// Keep the reader's mixed read state while real EventSource, htmx and
	// buildHandler deliver and apply production frames without interception.
	page.MustEvalOnNewDocument(`Object.defineProperty(document, 'visibilityState', {get: () => 'hidden'});
		document.addEventListener('htmx:sseMessage', e => {
			if (e.detail.type === 'sidebar') document.body.dataset.recounts = String(+(document.body.dataset.recounts || 0) + 1);
		});`)
	page.MustNavigate(f.reader.server.URL + f.path + "?before=3").MustWaitLoad()
	page.MustWait(`() => +document.body.dataset.recounts >= 1 && !document.querySelector('.htmx-settling')`)
	source, channel := view.TopicDOMID(f.source), view.ChannelDOMID(f.channel)
	if !page.MustEval(`id => document.getElementById('link-' + id).getAttribute('aria-current') === 'page' &&
		document.getElementById('topic-heading-count').textContent.trim() === ''`, source).Bool() {
		t.Fatal("listed selection or stable empty heading missing")
	}
	retained := page.MustEval(`() => document.querySelector('#sidebar-topics > li').id`).Str()
	// Corrupt both positive badges and an empty entry: a full recount must
	// restore exact counts, rather than incrementing stale DOM state.
	page.MustEval(`(source, channel, retained) => {
		for (const id of [source, channel, retained]) {
			const link = document.getElementById('link-' + id);
			// A badge is its hidden count next to its screen-reader text; the
			// row's other decorative parts are not it.
			let badge = link.querySelector('.sr-only')?.previousElementSibling;
			if (!badge) {
				const wrap = document.createElement('span'), label = document.createElement('span');
				badge = document.createElement('span');
				badge.setAttribute('aria-hidden', 'true');
				label.className = 'sr-only';
				wrap.append(badge, label);
				link.append(wrap);
			}
			badge.textContent = '999';
			badge.parentElement.hidden = false;
		}
		document.getElementById('link-' + channel).focus();
		window.focusedLink = document.activeElement;
		window.previousList = document.getElementById('sidebar-topics');
	}`, source, channel, retained)
	destination := branch(t, f.reader, view.ChannelURL("branch", f.channel), f.form, 303)
	destinationID := "topic-" + strings.ReplaceAll(destination[strings.LastIndex(destination, "/")+1:], "-", "")
	page.MustWait(`id => document.getElementById(id) && document.getElementById('sidebar-topics') !== window.previousList &&
		!document.querySelector('.htmx-settling')`, destinationID)
	if !page.MustEval(`(source, channel, retained, destination) => {
		// Scoped to the row's own link: a channel row also holds its topic rows.
		const count = node => node.querySelector('[data-unread-count]:not([hidden]) > .sr-only')?.previousElementSibling?.textContent || '';
		const badge = id => count(document.getElementById('link-' + id));
		const ids = Array.from(document.querySelectorAll('[id]'), e => e.id);
		return document.activeElement.id === 'link-' + channel && document.activeElement !== window.focusedLink &&
			new Set(ids).size === ids.length && document.querySelectorAll('#sidebar-topics > li').length === 50 &&
			!document.getElementById(source) && count(document.getElementById('topic-heading-count')) === '' && badge(channel) === '1' &&
			badge(destination) === '1' && badge(retained) === '' && !document.getElementById('stream-sink').hasChildNodes();
	}`, source, channel, retained, destinationID).Bool() {
		t.Fatal("production branch recount lost focus, retained stale counts/ids or exceeded the topic bound")
	}
	page.MustEval(`id => { document.getElementById('link-' + id).focus(); window.focusedLink = document.activeElement; }`, retained)
	post(t, f.owner, f.path, "unlisted source stays live")
	page.MustWait(`() => document.querySelector('#topic-heading-count [aria-hidden]')?.textContent === '1' &&
		!document.querySelector('.htmx-settling')`)
	if !page.MustEval(`id => document.activeElement.id === 'link-' + id && document.activeElement === window.focusedLink`, retained).Bool() {
		t.Fatal("increment replaced the retained topic link")
	}
	page.MustEval(`() => document.getElementById('sidebar-resync').dispatchEvent(new Event('sidebar-resync', {bubbles: true}))`)
	page.MustWait(`() => +document.body.dataset.recounts >= 3 && !document.querySelector('.htmx-settling')`)
	_, reload := f.reader.visit(t, "GET", f.path, nil, 200)
	if !page.MustEval(`markup => {
		const reload = new DOMParser().parseFromString(markup, 'text/html');
		return ['sidebar-channels', 'sidebar-topics', 'topic-heading-count'].every(id =>
			document.getElementById(id).isEqualNode(reload.getElementById(id)));
	}`, reload).Bool() {
		t.Fatal("production sidebar lists and unlisted heading differ from a reload")
	}
}
