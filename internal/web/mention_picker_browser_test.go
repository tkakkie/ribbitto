package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type mentionPickerResponse struct {
	http.ResponseWriter
	status int
}

func (w *mentionPickerResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

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
	type delayed struct {
		held, release, answered chan struct{}
		started, finished       sync.Once
	}
	delays := map[string]*delayed{}
	for _, q := range []string{"hold", "tomoy"} {
		delays[q] = &delayed{held: make(chan struct{}), release: make(chan struct{}), answered: make(chan struct{})}
	}
	stop := make(chan struct{})
	var searches, posts atomic.Int32
	var lastSearch atomic.Value
	lastSearch.Store("none")
	feed := view.ChannelURL("acme", f.Channel.ID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		if strings.HasSuffix(r.URL.Path, "/mention-candidates") {
			searches.Add(1)
			query := r.URL.Query().Get("q")
			lastSearch.Store(fmt.Sprintf("query %q pending", query))
			response := &mentionPickerResponse{ResponseWriter: w, status: http.StatusOK}
			w = response
			defer func() { lastSearch.Store(fmt.Sprintf("query %q status %d", query, response.status)) }()
			if delay, ok := delays[r.URL.Query().Get("q")]; ok {
				// Repeated queries must not panic and drop the HTTP connection.
				delay.started.Do(func() { close(delay.held) })
				select {
				case <-delay.release:
				case <-stop:
					return
				}
				defer delay.finished.Do(func() { close(delay.answered) })
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
	steps := newStepRunner(t, ctx, 5*time.Second)
	page, closePage := boundedPage(t, browser, ctx, `mention-picker step "create page"`)
	defer closePage()
	page = page.WithPanic(steps.fail)
	step := func(name string) { page = page.Context(steps.next(fmt.Sprintf("mention-picker step %q", name))) }
	press := func(name string, key input.Key) {
		t.Helper()
		step(name)
		steps.device(page, "dispatch keys", func(p *rod.Page) { p.Keyboard.MustType(key) })
	}
	await := func(name string, ch <-chan struct{}) {
		t.Helper()
		step(name)
		select {
		case <-ch:
		case <-page.GetContext().Done():
			steps.fail("waiting for delay channel: " + page.GetContext().Err().Error())
		}
	}
	step("navigate and install observers")
	page.MustNavigate(server.URL + feed).MustWaitLoad()
	page.MustEval(`() => { window.mentionInputs = 0; window.mentionSubmits = 0;
  document.getElementById('message-body').addEventListener('input', () => window.mentionInputs++);
  document.getElementById('message-body').addEventListener('compositionend', e => window.mentionEnded = e.timeStamp);
  window.addEventListener('submit', e => { if(e.target.id === 'message-composer') window.mentionSubmits++; }, true);
 }`)
	set := func(value string) {
		t.Helper()
		step(fmt.Sprintf("set composer %q", value))
		page.MustEval(`value => { const b = document.getElementById('message-body'), caret = value.includes('|') ? value.indexOf('|') : value.length; b.value = value.replace('|', ''); b.setSelectionRange(caret, caret); b.focus(); b.dispatchEvent(new Event('input', {bubbles:true})); }`, value)
	}
	openings := 0
	opened := func(query string, count int) {
		t.Helper()
		openings++
		step(fmt.Sprintf("open query %q with %d options (#%d)", query, count, openings))
		poll := time.NewTicker(25 * time.Millisecond)
		defer poll.Stop()
		// Keep the last snapshot: the step's context is unusable after timeout.
		state := "no DOM snapshot"
		for {
			result, err := page.Eval(`(q, n) => {
   const b = document.getElementById('message-body'), l = document.getElementById('mention-listbox');
   const hidden = document.getElementById('mention-picker')?.hidden;
   const options = document.querySelectorAll('#mention-listbox [role=option]').length;
   return {matches: hidden === false && l?.dataset.query === q && options === n,
    hidden, listbox: l?.outerHTML, query: l?.dataset.query, count: l?.dataset.count, options,
    activeElement: document.activeElement?.id, value: b?.value,
    selectionStart: b?.selectionStart, selectionEnd: b?.selectionEnd,
    activeDescendant: b?.getAttribute('aria-activedescendant'), mentionInputs: window.mentionInputs};
  }`, query, count)
			if err != nil {
				steps.fail(fmt.Sprintf("expected query %q / %d options: %v; last DOM: %s; searches=%d; last search=%v", query, count, err, state, searches.Load(), lastSearch.Load()))
			}
			state = result.Value.JSON("", "")
			if result.Value.Get("matches").Bool() {
				break
			}
			select {
			case <-poll.C:
			case <-page.GetContext().Done():
				steps.fail(fmt.Sprintf("expected query %q / %d options: %v; last DOM: %s; searches=%d; last search=%v", query, count, page.GetContext().Err(), state, searches.Load(), lastSearch.Load()))
			}
		}
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
			steps.fail(problem)
		}
	}
	closed := func() {
		t.Helper()
		if !page.MustEval(`() => document.getElementById('mention-picker').hidden && !document.getElementById('message-body').hasAttribute('aria-activedescendant') && !document.querySelector('#mention-listbox [aria-selected=true]') && document.getElementById('mention-status').textContent === ''`).Bool() {
			steps.fail("picker stayed open")
		}
	}
	set("@")
	opened("", 8)
	step("picker surface keeps textarea focus")
	if !page.MustEval(`() => ['pointerdown', 'mousedown'].every(type => { const e = new Event(type, {bubbles:true, cancelable:true}); document.getElementById('mention-picker').dispatchEvent(e); return e.defaultPrevented; })`).Bool() {
		steps.fail("picker surface press was not prevented")
	}
	first := page.MustElement(`#mention-listbox [aria-selected=true]`).MustAttribute("id")
	press("ArrowUp wraps to last option", input.ArrowUp)
	page.MustWait(`() => document.querySelector('#mention-listbox').lastElementChild.getAttribute('aria-selected') === 'true'`)
	press("ArrowDown wraps to first option", input.ArrowDown)
	if got := page.MustElement("#message-body").MustAttribute("aria-activedescendant"); got == nil || *got != *first {
		steps.fail("arrow keys did not wrap")
	}
	press("ArrowDown selects second option", input.ArrowDown)
	page.MustWait(`() => document.querySelectorAll('#mention-listbox [role=option]')[1].getAttribute('aria-selected') === 'true'`)
	press("Escape dismisses picker", input.Escape)
	closed()
	set("@")
	closed()
	for _, key := range []input.Key{input.Tab, input.Enter} {
		set("hello (@tom")
		opened("tom", 1)
		before := page.MustEval(`() => window.mentionInputs`).Int()
		press("pick with "+key.Info().Code, key)
		if page.MustEval(`() => window.mentionInputs`).Int() != before+1 {
			steps.fail("pick did not fire input")
		}
		closed()
		if got := page.MustEval(`() => { const b = document.getElementById('message-body'); return [b.value,b.selectionStart,b.selectionEnd,document.activeElement === b,window.mentionSubmits].join('|'); }`).Str(); got != "hello (@tomoya |15|15|true|0" {
			steps.fail(fmt.Sprintf("pick: %s", got))
		}
		set("")
	}
	set("@tom")
	opened("tom", 1)
	step("blur closes picker")
	page.MustEval(`() => document.getElementById('message-body').blur()`)
	closed()
	step("refocus reopens the same query")
	page.MustEval(`() => document.getElementById('message-body').focus()`)
	opened("tom", 1)
	step("click candidate")
	steps.device(page, "click candidate", func(p *rod.Page) { p.MustElement(`#mention-listbox [role=option]`).MustClick() })
	closed()
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@tomoya " || !page.MustEval(`() => document.activeElement.id === 'message-body'`).Bool() {
		steps.fail("mouse pick failed")
	}
	for _, key := range []input.Key{input.Tab, input.Enter} {
		set("")
		set("@tom")
		opened("tom", 1)
		before := page.MustEval(`() => window.mentionInputs`).Int()
		step("Shift+" + key.Info().Code + " does not pick")
		steps.device(page, "dispatch shifted key", func(p *rod.Page) { p.KeyActions().Press(input.ShiftLeft).Type(key).Release(input.ShiftLeft).MustDo() })
		value, inputs := "@tom", before
		if key == input.Enter {
			value, inputs = "@tom\n", before+1
		}
		if !page.MustEval(`(value, inputs) => document.getElementById('message-body').value === value && window.mentionInputs === inputs && window.mentionSubmits === 0`, value, inputs).Bool() {
			steps.fail("shifted key picked or sent")
		}
	}
	// Exercise each allowed boundary on its own, including Go's Unicode spaces.
	for _, value := range []string{"@tom", "x @tom", "x\t@tom", "x\n@tom", "x\u0085@tom", "x\u3000@tom", "(@tom", "`code` @tom", "\\` @tom", "```go\ncode\n```\n@tom", "x\n加藤 @tom| suffix"} {
		set("")
		set(value)
		opened("tom", 1)
	}
	step("select composer text")
	page.MustEval(`() => { const b = document.getElementById('message-body'); b.setSelectionRange(0, b.value.length); b.dispatchEvent(new Event('input', {bubbles:true})); }`)
	closed()
	step("change to phone viewport")
	page.MustSetViewport(390, 844, 1, true)
	set("@")
	opened("", 8)
	if !page.MustEval(`() => [...document.querySelectorAll('#mention-listbox [role=option]')].every(e => e.getBoundingClientRect().height >= 44)`).Bool() {
		steps.fail("small touch targets")
	}
	// Cases visible to the picker from conversation.TestPostingTypedHandles.
	for _, value := range []string{"plain", "x@tomoya", `\@tomoya`, "https://example.org/@tomoya", "https://example.org/(@tomoya", "a@tomoya.dev", "` @tomoya|`", "`@tomoya|`", "```\n@tomoya", "```go\n@tomoya", "```go\n@tomoya|\n```", "@tom oya", "@tom\toya", "@tom\u3000oya"} {
		before := searches.Load()
		set(value)
		closed()
		// Wait past the htmx debounce to prove invalid tokens send no search.
		page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
		if searches.Load() != before {
			steps.fail(fmt.Sprintf("search opened for %q", value))
		}
	}
	// A late response must remain hidden even before the next search is sent.
	set("@hold")
	await("hold response requested", delays["hold"].held)
	set("plain")
	close(delays["hold"].release)
	await("hold response answered", delays["hold"].answered)
	step("hidden late hold response swapped")
	page.MustWait(`() => document.getElementById('mention-listbox').dataset.query === 'hold'`)
	closed()
	set("@tom")
	opened("tom", 1)
	// A delayed answer opens, then input changes just before the pick, before
	// selectionchange can run. The pick must independently recheck its query.
	step("arm stale pick listener")
	page.MustEval(`() => { document.addEventListener('htmx:afterSwap', function stale(e) {
  if(e.detail.target?.id !== 'mention-listbox' || document.getElementById('mention-listbox').dataset.query !== 'tomoy') return;
  document.removeEventListener('htmx:afterSwap', stale);
  const b = document.getElementById('message-body'); b.value = '@other'; b.setSelectionRange(6,6);
  b.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', bubbles:true})); window.stalePickChecked = document.getElementById('mention-picker').hidden ? 'closed' : 'open';
 }); }`)
	set("@tomoy")
	await("stale pick response requested", delays["tomoy"].held)
	close(delays["tomoy"].release)
	step("stale pick checked inside tomoy swap")
	// Rod requires a Boolean result, not the recorded string's truthiness.
	page.MustWait(`() => typeof window.stalePickChecked === 'string'`)
	// Checked inside the handler: the changed query legitimately searches and
	// reopens the list for "other" a moment later.
	if page.MustEval(`() => window.stalePickChecked`).Str() != "closed" {
		steps.fail("stale pick left the picker open")
	}
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@other" {
		steps.fail("stale pick inserted")
	}
	set("@zzmissing")
	step("empty query hides picker but announces no members")
	page.MustWait(`() => { const b = document.getElementById('message-body'), l = document.getElementById('mention-listbox'); return document.getElementById('mention-picker').hidden && l.dataset.query === 'zzmissing' && !l.querySelector('[role=option]') && !b.hasAttribute('aria-activedescendant') && !!l.dataset.count && document.getElementById('mention-status').textContent === l.dataset.count; }`)
	press("empty results allow Tab navigation", input.Tab)
	closed()
	if page.MustEval(`() => document.activeElement.id === 'message-body'`).Bool() {
		steps.fail("empty results trapped Tab")
	}
	set("")
	before := searches.Load()
	step("flagged composing input sends no search")
	page.MustEval(`() => { const b = document.getElementById('message-body'); b.value = '@tom'; b.dispatchEvent(new InputEvent('input', {bubbles:true, isComposing:true})); }`)
	page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
	closed()
	if searches.Load() != before {
		steps.fail("composing input searched")
	}
	step("end synthetic composition")
	page.MustEval(`() => document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionend', {bubbles:true}))`)
	opened("tom", 1)
	set("@")
	opened("", 8)
	step("start native IME composition")
	if err := (proto.InputImeSetComposition{Text: "か", SelectionStart: 1, SelectionEnd: 1}).Call(page); err != nil {
		steps.fail(err)
	}
	closed()
	step("update native IME composition")
	if err := (proto.InputImeSetComposition{Text: "かとう", SelectionStart: 3, SelectionEnd: 3}).Call(page); err != nil {
		steps.fail(err)
	}
	enter := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13}
	step("dispatch IME Enter " + string(enter.Type))
	if err := enter.Call(page); err != nil {
		steps.fail(err)
	}
	step("commit native IME text")
	if err := (proto.InputInsertText{Text: "加藤"}).Call(page); err != nil {
		steps.fail(err)
	}
	enter.Type = proto.InputDispatchKeyEventTypeKeyUp
	step("dispatch IME Enter " + string(enter.Type))
	if err := enter.Call(page); err != nil {
		steps.fail(err)
	}
	opened("加藤", 1)
	if got := page.MustElement(`#mention-listbox [role=option]`).MustText(); got != "加藤 智也 @tomoya" {
		steps.fail(fmt.Sprintf("IME candidate: %q", got))
	}
	// Test the unflagged commit Enter at a fixed point inside the grace
	// period, even when the browser driver takes longer than 100 ms.
	step("suppress IME grace Enter and flagged keys")
	page.MustEval(`() => { const e = new KeyboardEvent('keydown', {key:'Enter', bubbles:true}); Object.defineProperty(e, 'timeStamp', {value:window.mentionEnded + 50}); document.getElementById('message-body').dispatchEvent(e); }`)
	page.MustEval(`() => { const b = document.getElementById('message-body'); for (const init of [{isComposing:true}, {keyCode:229}]) for (const key of ['ArrowDown','ArrowUp','Tab','Escape','Enter']) b.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles:true, ...init})); }`)
	opened("加藤", 1)
	if page.MustEval(`() => window.mentionSubmits`).Int() != 0 || posts.Load() != 0 || page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@加藤" {
		steps.fail("IME picked or sent")
	}
	step("leave IME grace period")
	page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 110))`)
	press("deliberate Enter picks after IME", input.Enter)
	closed()
	if page.MustEval(`() => document.getElementById('message-body').value`).Str() != "@tomoya " {
		steps.fail("next Enter did not pick")
	}
	press("Enter sends committed mention", input.Enter)
	page.MustWait(`() => window.mentionSubmits === 1 && document.getElementById('message-body').value === ''`)
	step("pending sends completed")
	page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	// Modified Enter closes without picking and reaches the existing send path.
	for i, key := range []input.Key{input.ControlLeft, input.MetaLeft} {
		set("@tom")
		opened("tom", 1)
		step("send with " + key.Info().Code + "+Enter")
		steps.device(page, "dispatch modified Enter", func(p *rod.Page) { p.KeyActions().Press(key).Type(input.Enter).Release(key).MustDo() })
		closed()
		page.MustWait(`n => window.mentionSubmits === n && document.getElementById('message-body').value === ''`, i+2)
		step("pending sends completed")
		page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	}
	step("verify stored IME and modified Enter posts")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	history, err := services.Messages.Page(page.GetContext(), m, f.Channel.ID, nil, nil)
	if err != nil {
		steps.fail(err)
	}
	ref := "<@" + realtime.FormatPayloadID(member) + ">"
	if len(history.Entries) != 3 || history.Entries[0].Body != ref || history.Entries[1].Body != "@tom" || history.Entries[2].Body != "@tom" {
		steps.fail(fmt.Sprintf("IME reference or modified Enter: %+v", history.Entries))
	}
	// A deliberate pick then send persists #1311's member reference.
	set("@tom")
	opened("tom", 1)
	press("deliberate Enter picks mention", input.Enter)
	press("Enter sends fourth post", input.Enter)
	page.MustWait(`() => window.mentionSubmits === 4 && document.getElementById('message-body').value === ''`)
	step("pending sends completed")
	page.MustWait(`() => !document.querySelector('[data-pending-form][data-state=sending], [data-pending-form][data-state=queued]')`)
	step("verify stored deliberate mention")
	history, err = services.Messages.Page(page.GetContext(), m, f.Channel.ID, nil, nil)
	if err != nil {
		steps.fail(err)
	}
	if len(history.Entries) != 4 || history.Entries[3].Body != ref {
		steps.fail(fmt.Sprintf("stored mention: %+v", history.Entries))
	}
}
