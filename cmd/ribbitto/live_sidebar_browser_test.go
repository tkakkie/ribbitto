package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestLiveSidebarBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "production sidebar acceptance")
	f := newSidebarBranchFixture(t)
	page, closePage := boundedPage(t, browser, ctx, "reader page")
	defer closePage()
	u, err := url.Parse(f.reader.server.URL)
	acceptanceOK(t, err)
	for _, cookie := range f.reader.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
	}
	// Keep the reader's mixed read state while real EventSource, htmx and
	// buildHandler deliver and apply production frames without interception.
	page.MustEvalOnNewDocument(`window.sources=[]; const Native=window.EventSource;
        window.EventSource=function(...args){const source=new Native(...args);window.sources.push(source);return source;};
        Object.setPrototypeOf(window.EventSource, Native); // htmx reads EventSource.CLOSED to reconnect.
        Object.defineProperty(document, 'visibilityState', {get: () => 'hidden'});
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
	// The shared helper expects creation redirects to "/"; this one goes to the new channel.
	response, _, err := f.owner.send(f.owner.request(t, "POST", "/organizations/branch/channels", url.Values{"name": {"other"}}))
	if err != nil || response.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating other channel: status %d, err %v", response.StatusCode, err)
	}
	otherBrowser := browser.Context(ctx).MustIncognito()
	defer otherBrowser.MustClose()
	other, closeOther := boundedPage(t, otherBrowser, ctx, "other channel page")
	defer closeOther()
	for _, cookie := range f.owner.client.Jar.Cookies(u) {
		other.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.owner.server.URL, Path: "/", Secure: true})
	}
	other.MustEvalOnNewDocument(sidebarUnreadBrowserClock + `;window.probe=0;
        Object.defineProperty(document,'visibilityState',{get:()=> 'hidden'});
        document.addEventListener('htmx:sseOpen', e => e.detail.source.addEventListener('unread', () => window.probe++));
        document.addEventListener('htmx:sseMessage', e => {if(e.detail.type==='sidebar') window.sidebarTest.recounts=(window.sidebarTest.recounts||0)+1;});`)
	other.MustNavigate(f.reader.server.URL + response.Header.Get("Location")).MustWaitLoad()
	other.MustWait(`() => window.sidebarTest.recounts === 1`)
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
	other.MustWait(`() => window.probe >= 1`)
	if other.MustEval(`() => window.sidebarTest.recounts`).Int() != 1 {
		t.Fatal("move recounted stream showing another channel")
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
	page.MustEval(`() => {const source=window.sources.at(-1);source.close();source.dispatchEvent(new Event('error'));}`)
	page.MustWait(`() => +document.body.dataset.recounts >= 4 && !document.querySelector('.htmx-settling')`)
	_, reload := f.reader.visit(t, "GET", f.path, nil, 200)
	if !page.MustEval(`markup => {
		const reload = new DOMParser().parseFromString(markup, 'text/html');
		return ['sidebar-channels', 'sidebar-topics', 'topic-heading-count'].every(id =>
			document.getElementById(id).isEqualNode(reload.getElementById(id)));
	}`, reload).Bool() {
		t.Fatal("production sidebar lists and unlisted heading differ from a reload")
	}
}
