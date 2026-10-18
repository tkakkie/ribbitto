package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testMentionPickerBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	member := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "tomoya@example.org", "加藤 智也"), org.RoleMember, "tomoya", 1)
	for i := range 8 {
		orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, fmt.Sprintf("person%d@example.org", i), fmt.Sprintf("Person %d", i)), org.RoleMember, fmt.Sprintf("person%d", i), 1)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	cookie, _, err := sessions.Create(ctx, f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	services := postgresServices(t, pool, sessions, "", false)
	// This scenario makes many deliberate searches without exercising throttling.
	services.MentionLimits = middleware.NewMemberRateLimiterWithBudget(time.Now, 100, 100, 2)
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	type delayed struct{ held, release, answered chan struct{} }
	delays := map[string]delayed{}
	for _, q := range []string{"hold", "tomoy"} {
		delays[q] = delayed{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	}
	stop := make(chan struct{})
	var searches, posts atomic.Int32
	feed := view.ChannelURL("acme", f.Channel.ID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		if strings.HasSuffix(r.URL.Path, "/mention-candidates") {
			searches.Add(1)
			if delay, ok := delays[r.URL.Query().Get("q")]; ok {
				close(delay.held)
				select {
				case <-delay.release:
				case <-stop:
					return
				}
				defer close(delay.answered)
			}
		}
		if r.Method == "POST" && r.URL.Path == feed {
			posts.Add(1)
		}
		// Real stream transport is unnecessary for the persisted-body assertion.
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer close(stop)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustNavigate(server.URL + feed).MustWaitLoad()
	page.MustEval(`() => { window.mentionInputs = 0; window.mentionSubmits = 0;
  document.getElementById('message-body').addEventListener('input', () => window.mentionInputs++);
  document.getElementById('message-body').addEventListener('compositionend', e => window.mentionEnded = e.timeStamp);
  window.addEventListener('submit', e => { if(e.target.id === 'message-composer') window.mentionSubmits++; }, true);
 }`)
	set := func(value string) {
		t.Helper()
		page.MustEval(`value => { const b = document.getElementById('message-body'), caret = value.includes('|') ? value.indexOf('|') : value.length; b.focus(); b.value = value.replace('|', ''); b.setSelectionRange(caret, caret); b.dispatchEvent(new Event('input', {bubbles:true})); }`, value)
	}
	opened := func(query string, count int) {
		t.Helper()
		page.MustWait(`(q, n) => !document.getElementById('mention-picker').hidden && document.getElementById('mention-listbox').dataset.query === q && document.querySelectorAll('#mention-listbox [role=option]').length === n`, query, count)
		if problem := page.MustEval(`() => {
   const b = document.getElementById('message-body'), l = document.getElementById('mention-listbox');
   const selected = l.querySelectorAll('[aria-selected=true]');
   if(document.querySelectorAll('#mention-listbox').length !== 1 || l.querySelector('ul')) return 'nested listbox';
   if(b.getAttribute('role') || b.getAttribute('aria-autocomplete') !== 'list' || b.getAttribute('aria-controls') !== l.id) return 'textbox semantics';
   if(selected.length !== Math.min(1, l.children.length) || b.getAttribute('aria-activedescendant') !== (selected[0]?.id ?? null) || document.activeElement !== b) return 'active option or focus';
   if(document.getElementById('mention-status').textContent !== l.dataset.count || document.getElementById('mention-status').getAttribute('aria-live') !== 'polite') return 'count announcement';
   if(document.getElementById('mention-search').closest('#message-composer, #message-composer-state')) return 'search inside composer';
   return '';
  }`).Str(); problem != "" {
			t.Fatal(problem)
		}
	}
	closed := func() {
		t.Helper()
		if !page.MustEval(`() => document.getElementById('mention-picker').hidden && !document.getElementById('message-body').hasAttribute('aria-activedescendant') && !document.querySelector('#mention-listbox [aria-selected=true]') && document.getElementById('mention-status').textContent === ''`).Bool() {
			t.Fatal("picker stayed open")
		}
	}
	set("@")
	opened("", 8)
	first := page.MustElement(`#mention-listbox [aria-selected=true]`).MustAttribute("id")
	page.Keyboard.MustType(input.ArrowUp)
	page.MustWait(`() => document.querySelector('#mention-listbox').lastElementChild.getAttribute('aria-selected') === 'true'`)
	page.Keyboard.MustType(input.ArrowDown)
	if got := page.MustElement("#message-body").MustAttribute("aria-activedescendant"); got == nil || *got != *first {
		t.Fatal("arrow keys did not wrap")
	}
	page.Keyboard.MustType(input.ArrowDown)
	page.MustWait(`() => document.querySelectorAll('#mention-listbox [role=option]')[1].getAttribute('aria-selected') === 'true'`)
	page.Keyboard.MustType(input.Escape)
	closed()
	set("@")
	closed()
	for _, key := range []input.Key{input.Tab, input.Enter} {
		set("hello (@tom")
		opened("tom", 1)
		before := page.MustEval(`() => window.mentionInputs`).Int()
		page.Keyboard.MustType(key)
		if page.MustEval(`() => window.mentionInputs`).Int() != before+1 {
			t.Fatal("pick did not fire input")
		}
		closed()
		if got := page.MustEval(`() => { const b = document.getElementById('message-body'); return [b.value,b.selectionStart,b.selectionEnd,document.activeElement === b,window.mentionSubmits].join('|'); }`).Str(); got != "hello (@tomoya |15|15|true|0" {
			t.Fatalf("pick: %s", got)
		}
		set("")
	}
	set("@tom")
	opened("tom", 1)
	page.MustElement(`#mention-listbox [role=option]`).MustClick()
	closed()
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@tomoya " || !page.MustEval(`() => document.activeElement.id === 'message-body'`).Bool() {
		t.Fatal("mouse pick failed")
	}
	// Exercise each allowed boundary on its own, including Go's Unicode spaces.
	for _, value := range []string{"@tom", "x @tom", "x\t@tom", "x\n@tom", "x\u0085@tom", "x\u3000@tom", "(@tom", "`code` @tom", "\\` @tom", "```go\ncode\n```\n@tom", "x\n加藤 @tom| suffix"} {
		set("")
		set(value)
		opened("tom", 1)
	}
	page.MustEval(`() => { const b = document.getElementById('message-body'); b.setSelectionRange(0, b.value.length); b.dispatchEvent(new Event('input', {bubbles:true})); }`)
	closed()
	page.MustSetViewport(390, 844, 1, true)
	set("@")
	opened("", 8)
	if !page.MustEval(`() => [...document.querySelectorAll('#mention-listbox [role=option]')].every(e => e.getBoundingClientRect().height >= 44)`).Bool() {
		t.Fatal("small touch targets")
	}
	// Cases visible to the picker from conversation.TestPostingTypedHandles.
	for _, value := range []string{"plain", "x@tomoya", `\@tomoya`, "https://example.org/@tomoya", "https://example.org/(@tomoya", "a@tomoya.dev", "` @tomoya|`", "`@tomoya|`", "```\n@tomoya", "```go\n@tomoya", "```go\n@tomoya|\n```", "@tom oya", "@tom\toya", "@tom\u3000oya"} {
		before := searches.Load()
		set(value)
		closed()
		// Wait past the htmx debounce to prove invalid tokens send no search.
		page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
		if searches.Load() != before {
			t.Fatalf("search opened for %q", value)
		}
	}
	// A late response must remain hidden even before the next search is sent.
	set("@hold")
	select {
	case <-delays["hold"].held:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	set("plain")
	close(delays["hold"].release)
	select {
	case <-delays["hold"].answered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	page.MustWait(`() => document.getElementById('mention-listbox').dataset.query === 'hold'`)
	closed()
	set("@tom")
	opened("tom", 1)
	// A delayed answer opens, then input changes just before the pick, before
	// selectionchange can run. The pick must independently recheck its query.
	page.MustEval(`() => { document.addEventListener('htmx:afterSwap', function stale(e) {
  if(e.detail.target?.id !== 'mention-listbox' || document.getElementById('mention-listbox').dataset.query !== 'tomoy') return;
  document.removeEventListener('htmx:afterSwap', stale);
  const b = document.getElementById('message-body'); b.value = '@other'; b.setSelectionRange(6,6);
  b.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', bubbles:true})); window.stalePickChecked = document.getElementById('mention-picker').hidden ? 'closed' : 'open';
 }); }`)
	set("@tomoy")
	select {
	case <-delays["tomoy"].held:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(delays["tomoy"].release)
	page.MustWait(`() => window.stalePickChecked`)
	// Checked inside the handler: the changed query legitimately searches and
	// reopens the list for "other" a moment later.
	if page.MustEval(`() => window.stalePickChecked`).Str() != "closed" {
		t.Fatal("stale pick left the picker open")
	}
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@other" {
		t.Fatal("stale pick inserted")
	}
	set("@zzmissing")
	opened("zzmissing", 0)
	page.Keyboard.MustType(input.Tab)
	closed()
	if page.MustEval(`() => document.activeElement.id === 'message-body'`).Bool() {
		t.Fatal("empty results trapped Tab")
	}
	set("")
	before := searches.Load()
	page.MustEval(`() => { const b = document.getElementById('message-body'); b.value = '@tom'; b.dispatchEvent(new InputEvent('input', {bubbles:true, isComposing:true})); }`)
	page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
	closed()
	if searches.Load() != before {
		t.Fatal("composing input searched")
	}
	page.MustEval(`() => document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionend', {bubbles:true}))`)
	opened("tom", 1)
	set("@")
	opened("", 8)
	if err := (proto.InputImeSetComposition{Text: "か", SelectionStart: 1, SelectionEnd: 1}).Call(page); err != nil {
		t.Fatal(err)
	}
	closed()
	if err := (proto.InputImeSetComposition{Text: "かとう", SelectionStart: 3, SelectionEnd: 3}).Call(page); err != nil {
		t.Fatal(err)
	}
	enter := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13}
	if err := enter.Call(page); err != nil {
		t.Fatal(err)
	}
	if err := (proto.InputInsertText{Text: "加藤"}).Call(page); err != nil {
		t.Fatal(err)
	}
	enter.Type = proto.InputDispatchKeyEventTypeKeyUp
	if err := enter.Call(page); err != nil {
		t.Fatal(err)
	}
	opened("加藤", 1)
	if got := page.MustElement(`#mention-listbox [role=option]`).MustText(); got != "加藤 智也 @tomoya" {
		t.Fatalf("IME candidate: %q", got)
	}
	// Test the unflagged commit Enter at a fixed point inside the grace
	// period, even when the browser driver takes longer than 100 ms.
	page.MustEval(`() => { const e = new KeyboardEvent('keydown', {key:'Enter', bubbles:true}); Object.defineProperty(e, 'timeStamp', {value:window.mentionEnded + 50}); document.getElementById('message-body').dispatchEvent(e); }`)
	page.MustEval(`() => { const b = document.getElementById('message-body'); for (const init of [{isComposing:true}, {keyCode:229}]) for (const key of ['ArrowDown','ArrowUp','Tab','Escape','Enter']) b.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles:true, ...init})); }`)
	opened("加藤", 1)
	if page.MustEval(`() => window.mentionSubmits`).Int() != 0 || posts.Load() != 0 || page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@加藤" {
		t.Fatal("IME picked or sent")
	}
	page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 110))`)
	page.Keyboard.MustType(input.Enter)
	closed()
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@tomoya " {
		t.Fatal("next Enter did not pick")
	}
	page.Keyboard.MustType(input.Enter)
	page.MustWait(`() => window.mentionSubmits === 1 && document.getElementById('message-body').value === ''`)
	page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	// Modified Enter closes without picking and reaches the existing send path.
	for i, key := range []input.Key{input.ControlLeft, input.MetaLeft} {
		set("@tom")
		opened("tom", 1)
		page.KeyActions().Press(key).Type(input.Enter).Release(key).MustDo()
		closed()
		page.MustWait(`n => window.mentionSubmits === n && document.getElementById('message-body').value === ''`, i+2)
		page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	}
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	history, err := services.Messages.Page(ctx, m, f.Channel.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := "<@" + realtime.FormatPayloadID(member) + ">"
	if len(history.Entries) != 3 || history.Entries[0].Body != ref || history.Entries[1].Body != "@tom" || history.Entries[2].Body != "@tom" {
		t.Fatalf("IME reference or modified Enter: %+v", history.Entries)
	}
	// A deliberate pick then send persists #1311's member reference.
	set("@tom")
	opened("tom", 1)
	page.Keyboard.MustType(input.Enter)
	page.Keyboard.MustType(input.Enter)
	page.MustWait(`() => window.mentionSubmits === 4 && document.getElementById('message-body').value === ''`)
	page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	history, err = services.Messages.Page(ctx, m, f.Channel.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) != 4 || history.Entries[3].Body != ref {
		t.Fatalf("stored mention: %+v", history.Entries)
	}
}
