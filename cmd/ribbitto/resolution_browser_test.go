package main

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

func TestResolutionBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "resolution flow")
	f := newSidebarBranchFixture(t)
	actor, closeActor := boundedPage(t, browser, ctx, "native forms")
	defer closeActor()
	u, err := url.Parse(f.reader.server.URL)
	acceptanceOK(t, err)
	for _, cookie := range f.reader.client.Jar.Cookies(u) {
		actor.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
	}
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
	if !actor.MustEval(`message => document.querySelector('#resolution-summary a')?.getAttribute('href') === '/organizations/branch/messages/' + message &&
		document.querySelector('#resolution-summary').textContent.includes('unread moves') &&
		document.querySelector('#topic-heading-count').textContent.includes('Resolved') &&
		document.querySelector('#sidebar-topics a[aria-current=page]').textContent.includes('Resolved')`, message).Bool() {
		t.Fatal("native redirect lost conclusion, stable link or marks")
	}
	// A new reply preserves the conclusion; another choice replaces it.
	post(t, f.owner, f.path, "later reply")
	actor.MustNavigate(f.reader.server.URL + f.path).MustWaitLoad()
	if !actor.MustEval(`() => document.querySelector('#resolution-summary p')?.textContent === 'unread moves'`).Bool() {
		t.Fatal("posting reopened the topic")
	}
	replacement := actor.MustEval(`() => document.querySelector('#message-items > li:last-child a[data-message-action]').getAttribute('data-message-action')`).Str()
	actor.MustNavigate(f.reader.server.URL + "/organizations/branch/messages/" + replacement + "/actions").MustWaitLoad()
	submit(`button[name=message]`)
	if !actor.MustEval(`() => document.querySelector('#resolution-summary p')?.textContent === 'later reply'`).Bool() {
		t.Fatal("native replacement lost conclusion")
	}
	submit(`#resolution-summary button`)
	if !actor.MustEval(`() => !document.querySelector('#resolution-summary a') && !document.querySelector('#topic-heading-count').textContent.includes('Resolved')`).Bool() {
		t.Fatal("native Reopen retained conclusion or mark")
	}
}
