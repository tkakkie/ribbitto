package main

import (
	"context"
	"net/url"
	"strings"
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
	actor.Keyboard.MustType(input.Tab, input.Tab, input.Tab)
	if !actor.MustEval(`() => document.activeElement.matches('button[name=reply]')`).Bool() {
		t.Fatal("native Reply is unreachable by Tab")
	}
	actor.Keyboard.MustType(input.Tab, input.Tab)
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
	sibling := actor.MustEval(`() => [...document.querySelectorAll('#sidebar-topics a')].find(a => !a.hasAttribute('aria-current') && a.textContent.includes('topic01')).getAttribute('href')`).Str()
	actor.MustNavigate(f.reader.server.URL + sibling).MustWaitLoad()
	if !actor.MustEval(`path => {
  const rows = [...document.querySelectorAll('#sidebar-topics a')];
  return rows.find(a => a.getAttribute('href') === path)?.textContent.includes('Resolved') &&
   rows.filter(a => !a.hasAttribute('aria-current') && a.getAttribute('href') !== path).every(a => !a.textContent.includes('Resolved'));
 }`, f.path).Bool() {
		t.Fatal("unselected sidebar rows lost their resolution state")
	}
	// A new reply preserves the conclusion; another choice replaces it.
	conclusion := strings.TrimSpace(strings.Repeat("later reply\n", 20))
	post(t, f.owner, f.path, conclusion)
	actor.MustNavigate(f.reader.server.URL + f.path).MustWaitLoad()
	if !actor.MustEval(`() => document.querySelector('#resolution-summary p')?.textContent === 'unread moves'`).Bool() {
		t.Fatal("posting reopened the topic")
	}
	replacement := actor.MustEval(`() => document.querySelector('#message-items > li:last-child a[data-message-action]').getAttribute('data-message-action')`).Str()
	actor.MustNavigate(f.reader.server.URL + "/organizations/branch/messages/" + replacement + "/actions").MustWaitLoad()
	submit(`button[name=message]`)
	if !actor.MustEval(`text => document.querySelector('#resolution-summary p')?.textContent === text`, conclusion).Bool() {
		t.Fatal("native replacement lost conclusion")
	}
	actor.MustSetViewport(844, 390, 1, true)
	actor.MustEval(`() => document.querySelector('#resolution-summary p').focus()`)
	actor.Keyboard.MustType(input.End)
	actor.MustWait(`() => document.querySelector('#resolution-summary p').scrollTop > 0`)
	if !actor.MustEval(`() => {
  const summary = document.querySelector('#resolution-summary p');
  return document.activeElement === summary && summary.scrollTop > 0 && summary.clientHeight <= 64;
 }`).Bool() {
		t.Fatal("long conclusion cannot be scrolled by keyboard on a landscape phone")
	}
	actor.MustSetViewport(390, 844, 1, true)
	submit(`#resolution-summary button`)
	if !actor.MustEval(`() => !document.querySelector('#resolution-summary a') && !document.querySelector('#topic-heading-count').textContent.includes('Resolved')`).Bool() {
		t.Fatal("native Reopen retained conclusion or mark")
	}
}
