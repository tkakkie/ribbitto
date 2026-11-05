package main

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestResolutionLiveBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "resolution flow")
	f := newSidebarBranchFixture(t)
	// 51 topics: resolving the last one must displace it from the bounded list.
	branch(t, f.owner, view.ChannelURL("branch", f.channel), url.Values{
		"message": {f.form["message"][0]}, "from": f.form["from"], "name": {"aaa"},
	}, 303)
	page, closePage := boundedPage(t, browser, ctx, "live observer")
	defer closePage()
	actor, closeActor := boundedPage(t, browser, ctx, "native forms")
	defer closeActor()
	u, err := url.Parse(f.reader.server.URL)
	acceptanceOK(t, err)
	for _, cookie := range f.reader.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
	}
	page.MustEvalOnNewDocument(`Object.defineProperty(document, 'visibilityState', {get: () => 'hidden'}); document.addEventListener('htmx:sseOpen', () => window.connected = true);`)
	page.MustNavigate(f.reader.server.URL + f.path).MustWaitLoad()
	page.MustWait(`() => window.connected === true`)
	acceptanceOK(t, (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(actor))
	message := f.form["message"][1]
	actions := f.reader.server.URL + "/organizations/branch/messages/" + message + "/actions"
	actor.MustSetViewport(390, 844, 1, true)
	acceptanceOK(t, (proto.EmulationSetEmulatedVisionDeficiency{Type: "achromatopsia"}).Call(actor))
	actor.MustNavigate(actions).MustWaitLoad()
	actor.Keyboard.MustType(input.Tab, input.Tab, input.Tab, input.Tab)
	if !actor.MustEval(`() => document.activeElement.matches('button[name=message]') && document.documentElement.scrollWidth <= innerWidth`).Bool() {
		t.Fatal("native Resolve is unreachable by Tab or overflows the phone")
	}
	// Native Enter submits with scripts disabled; Rod can still focus through CDP.
	submit := func(selector string) {
		t.Helper()
		actor.MustEval(`s => document.querySelector(s).focus()`, selector)
		wait := actor.WaitNavigation(proto.PageLifecycleEventNameLoad)
		actor.Keyboard.MustType(input.Enter)
		wait()
	}
	submit(`button[name=message]`)
	page.MustWait(`message => document.querySelector('#resolution-summary a')?.getAttribute('href') === '/organizations/branch/messages/' + message`, message)
	if !actor.MustEval(`() => document.querySelector('#resolution-summary').textContent.includes('unread moves')`).Bool() {
		t.Fatal("native redirect lost conclusion")
	}
	if !page.MustEval(`id => document.querySelectorAll('#sidebar-topics > li').length === 50 && !document.getElementById(id) &&
		document.querySelector('#topic-heading-count').textContent.includes('Resolved') && document.querySelector('#topic-heading-count [data-unread-count]').getAttribute('data-unread-count') === '2'`, view.TopicDOMID(f.source)).Bool() {
		t.Fatal("unlisted selected resolution mark lost")
	}
	// A new unread reply preserves the conclusion. Replacement refreshes its text.
	post(t, f.owner, f.path, "later reply")
	page.MustWait(`() => document.querySelector('#message-items').textContent.includes('later reply')`)
	_, html := f.reader.visit(t, "GET", f.path, nil, 200)
	if !strings.Contains(html, "Resolved") || !strings.Contains(html, "/messages/"+message) {
		t.Fatal("posting reopened the topic")
	}
	replacement := page.MustEval(`() => document.querySelector('#message-items > li:last-child a[data-message-action]').getAttribute('data-message-action')`).Str()
	actor.MustNavigate(f.reader.server.URL + "/organizations/branch/messages/" + replacement + "/actions").MustWaitLoad()
	submit(`button[name=message]`)
	page.MustWait(`() => document.querySelector('#resolution-summary p')?.textContent === 'later reply'`)
	// Reconnect reads the current conclusion without reconstructing an old event.
	page.MustReload().MustWaitLoad()
	page.MustWait(`() => document.querySelector('#resolution-summary p')?.textContent === 'later reply'`)
	submit(`#resolution-summary button`)
	page.MustWait(`() => !document.querySelector('#resolution-summary a') && !document.querySelector('#topic-heading-count').textContent.includes('Resolved')`)
	acceptanceOK(t, (proto.EmulationSetScriptExecutionDisabled{Value: false}).Call(actor))
	channelPath := strings.Split(f.path, "/topics/")[0]
	post(t, f.owner, channelPath, "default reply")
	for _, path := range []string{f.path, channelPath} {
		actor.MustEvalOnNewDocument(`window.menuErrors = []; addEventListener('error', e => window.menuErrors.push(e.message));`)
		actor.MustNavigate(f.reader.server.URL + path).MustWaitLoad()
		actor.MustElement(`a[data-message-action="` + message + `"] + button`).MustClick()
		if path == f.path {
			if !actor.MustEval(`(path, message) => {
   const form = document.querySelector('#resolution-form');
   return form.checkVisibility() && form.getAttribute('action') === path + '/resolve' && form.querySelector('button').value === message;
  }`, f.path, message).Bool() {
				t.Fatal("topic message menu lost Resolve action or identity")
			}
		} else {
			if !actor.MustEval(`() => !document.querySelector('#resolution-form, #message-items [data-resolve-url]') && window.menuErrors.length === 0`).Bool() {
				t.Fatal("channel feed retained Resolve markup or threw opening its menu")
			}
			// The existing topic label reaches Resolve without another feed control.
			actor.MustEval(`() => document.querySelector('#message-actions').hidePopover()`)
			actor.MustElement(`#message-items > li:last-child a[data-message-action] + button`).MustClick()
			if !actor.MustEval(`() => !document.querySelector('#resolution-form') && window.menuErrors.length === 0`).Bool() {
				t.Fatal("default message menu offers Resolve or throws")
			}
			actor.MustEval(`() => document.querySelector('#message-actions').hidePopover()`)
			actor.MustElement(`#message-` + strings.ReplaceAll(message, "-", "") + ` a[href="` + f.path + `"]`).MustClick()
			actor.MustWait(`() => !!document.querySelector('#message-items[data-resolve-url]')`)
		}
	}

	actor.MustNavigate(f.reader.server.URL + f.path).MustWaitLoad()
	actor.MustElement(`#message-items > li:last-child button[popovertarget="message-actions"]`).MustClick()
	actor.MustElement(`#resolution-form button`).MustClick()
	page.MustWait(`() => document.querySelector('#resolution-summary p')?.textContent === 'later reply'`)
}

func TestResolutionSelectedTopicReachability(t *testing.T) {
	f := newSidebarBranchFixture(t)
	branch(t, f.owner, view.ChannelURL("branch", f.channel), url.Values{
		"message": {f.form["message"][0]}, "from": f.form["from"], "name": {"aaa"},
	}, 303)
	response, _, err := f.owner.send(f.owner.request(t, "POST", f.path+"/resolve", url.Values{"message": {f.form["message"][1]}}))
	acceptanceOK(t, err)
	if response.StatusCode != 303 || response.Header.Get("Location") != f.path {
		t.Fatal("conclusion choice did not redirect to its topic")
	}
	_, feed := f.reader.visit(t, "GET", view.ChannelURL("branch", f.channel), nil, 200)
	list := sidebarNode(t, feed, "sidebar-topics")
	if strings.Count(list, "<li ") != 50 || strings.Contains(list, view.TopicDOMID(f.source)) {
		t.Fatal("concluded last topic must be outside the 50 listed topics")
	}
	// Selecting the URL exercises SidebarSnapshot.Topic beyond the bounded list.
	_, selected := f.reader.visit(t, "GET", f.path, nil, 200)
	if !strings.Contains(sidebarNode(t, selected, "topic-heading-count"), "Resolved") ||
		!strings.Contains(sidebarNode(t, selected, "resolution-summary"), "unread moves") {
		t.Fatal("unlisted selection lost its mark or conclusion")
	}
	sidebarBadge(t, selected, "topic-heading-count", 2)
}
