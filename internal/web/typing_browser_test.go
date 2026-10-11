package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type browserSignal struct {
	values  url.Values
	release chan struct{}
	done    chan struct{}
}

func testTypingComposerBrowser(t *testing.T, browser *rod.Browser) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%t", retry), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			handler, state, member, place := composerHandler(t, false)
			defer state.Open(member.Organization.ID, member.Member.ID, place.Channel)()
			var signals, posts atomic.Int32
			requests := make(chan browserSignal, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				if r.Method != "POST" {
					handler.ServeHTTP(w, r)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/typing") {
					posts.Add(1)
					handler.ServeHTTP(w, r)
					return
				}
				n := signals.Add(1)
				signal := browserSignal{r.PostForm, make(chan struct{}), make(chan struct{})}
				requests <- signal
				if n > 1 {
					select {
					case <-signal.release:
					case <-r.Context().Done():
						return
					}
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if response.Code != 204 || response.Body.Len() != 0 {
					t.Errorf("signal response: %d %q", response.Code, response.Body.String())
				}
				w.WriteHeader(response.Code)
				close(signal.done)
			}))
			defer server.Close()
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			// Own only the throttle timer in this harness: advance it through a
			// gate so contention coverage never relies on scheduler sleeps.
			page.MustEvalOnNewDocument(`(() => {
 const timers = [], original = window.setTimeout;
 window.setTimeout = (fn, ms, ...args) => ms === 2000 ? timers.push(() => fn(...args)) : original(fn, ms, ...args);
 window.advanceTypingThrottle = () => { while (timers.length) timers.shift()(); };
 document.addEventListener('htmx:beforeRequest', e => {
  if (e.detail.elt.id === 'typing-form') document.body.dataset.signals = String(+(document.body.dataset.signals || 0) + 1);
 });
 // The out-of-band typing form settles a second time for the same request.
 const settled = new WeakSet();
 document.addEventListener('htmx:afterSettle', e => {
  if (e.detail.target.id !== 'message-composer' || settled.has(e.detail.xhr)) return;
  settled.add(e.detail.xhr);
  document.body.dataset.posts = String(+(document.body.dataset.posts || 0) + 1);
 });
})()`)
			page.MustNavigate(server.URL + view.ChannelURL("acme", place.Channel)).MustWaitLoad()
			token := func() string {
				return *page.MustElement(`#message-composer input[name="composer"]`).MustAttribute("value")
			}
			a := token()
			next := func(want string) browserSignal {
				t.Helper()
				select {
				case signal := <-requests:
					if len(signal.values) != 1 || len(signal.values["composer"]) != 1 || signal.values.Get("composer") != want {
						t.Fatalf("signal leaked draft or wrong generation: %v", signal.values)
					}
					return signal
				case <-ctx.Done():
					t.Fatal("signal did not arrive", ctx.Err())
					return browserSignal{}
				}
			}
			waitDone := func(signal browserSignal) {
				t.Helper()
				select {
				case <-signal.done:
				case <-ctx.Done():
					t.Fatal("signal did not complete", ctx.Err())
				}
			}
			page.MustEval(`() => {
 const body = document.getElementById('message-body');
 document.querySelector('#message-composer input[name=composer]').dispatchEvent(new InputEvent('input', {bubbles:true}));
 body.value = '  '; body.dispatchEvent(new InputEvent('input', {bubbles:true}));
 body.value = 'IME draft'; body.dispatchEvent(new InputEvent('input', {bubbles:true, isComposing:true}));
 if (document.body.dataset.signals) throw new Error('unrelated, blank or composing input sent a signal');
 body.dispatchEvent(new CompositionEvent('compositionend', {bubbles:true}));
}`)
			waitDone(next(a))
			page.MustWait(`() => !document.getElementById('typing-form').classList.contains('htmx-request')`)
			if len(state.Read(member.Organization.ID, place, kernel.ID{99}).Typists) != 1 {
				t.Fatal("IME completion did not signal typing")
			}
			page.MustEval(`() => {
 const body = document.getElementById('message-body');
 for (let i = 0; i < 5; i++) body.dispatchEvent(new InputEvent('input', {bubbles:true}));
 if (document.body.dataset.signals !== '1') throw new Error('input burst bypassed throttle');
 window.advanceTypingThrottle(); body.dispatchEvent(new InputEvent('input', {bubbles:true}));
}`)
			delayed := next(a)
			page.MustEval(`() => {
 window.oldSignal = document.getElementById('typing-form');
 window.advanceTypingThrottle();
 document.getElementById('message-body').dispatchEvent(new InputEvent('input', {bubbles:true}));
 if (document.body.dataset.signals !== '2') throw new Error('in-flight signal overlapped');
 if (document.getElementById('message-body').disabled || document.querySelector('#message-composer button').disabled) throw new Error('signal disabled composer');
}`)
			if retry {
				page.MustEval(`() => { document.getElementById('message-body').value = '  '; document.getElementById('message-composer').requestSubmit(); }`)
				page.MustWait(`() => document.body.dataset.posts === '1' && !document.querySelector('.htmx-settling')`)
				if token() != a || page.MustElement("#message-body").MustProperty("value").Str() != "  " {
					t.Fatal("422 lost generation or draft")
				}
			}
			page.MustEval(`() => { document.getElementById('message-body').value = 'successful draft'; document.getElementById('message-composer').requestSubmit(); }`)
			wantPosts := 1
			if retry {
				wantPosts++
			}
			page.MustWait(`n => +document.body.dataset.posts === n && !document.querySelector('.htmx-settling')`, wantPosts)
			b := token()
			if b == a || b == "" || posts.Load() != int32(wantPosts) || len(state.Read(member.Organization.ID, place, kernel.ID{99}).Typists) != 0 {
				t.Fatal("post dropped, did not close typing, or retained generation")
			}
			page.MustEval(`b => {
 const form = document.getElementById('typing-form'), body = document.getElementById('message-body');
 if (document.querySelectorAll('#typing-form').length !== 1 || form === window.oldSignal || form.elements.composer.value !== b || body.value !== '' || body.disabled || document.querySelector('#message-composer button').disabled || document.activeElement !== body) throw new Error('replacement contract');
 body.value = 'next draft'; body.dispatchEvent(new InputEvent('input', {bubbles:true}));
}`, b)
			fresh := next(b) // New form sends independently while the old request is still held.
			close(delayed.release)
			waitDone(delayed)
			if len(state.Read(member.Organization.ID, place, kernel.ID{99}).Typists) != 0 {
				t.Fatal("pre-post signal revived typing after success")
			}
			close(fresh.release)
			waitDone(fresh)
			page.MustWait(`() => !document.getElementById('typing-form').classList.contains('htmx-request')`)
			page.MustEval(`() => {
 window.advanceTypingThrottle();
 if (document.body.dataset.signals !== '3' || document.getElementById('message-body').value !== 'next draft' || document.getElementById('message-body').disabled) throw new Error('signal queued, swapped, or disabled draft');
}`)
			if signals.Load() != 3 || posts.Load() != int32(wantPosts) || len(state.Read(member.Organization.ID, place, kernel.ID{99}).Typists) != 1 {
				t.Fatal("fresh input refused or unexpected queued request/post")
			}
		})
	}
}

// Production handlers, lifecycle, owner and writer; membership/history/storage are DB-free fixtures.
func testTypingIndicatorBrowser(t *testing.T, browser *rod.Browser) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			hub := realtime.NewHub()
			state := typing.New(hub)
			closed := make(chan kernel.ID, 16)
			signaled := make(chan struct{}, 8)
			type connection struct {
				cancel        context.CancelFunc
				after, lastID string
			}
			type screen struct {
				page     *rod.Page
				requests chan connection
			}
			open := func(member byte, topic *kernel.ID) screen {
				t.Helper()
				requests := make(chan connection, 4)
				handler, _, m, place := composerHandler(t, true, func(s *Services) {
					m := org.Membership{Organization: org.Organization{ID: kernel.ID{7}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{member}, Handle: "alice"}}
					s.Authz, s.Typing = composerMember{membership: m}, state
					snapshot := conversation.ChannelPage{Current: conversation.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}}, EventCursor: new(int64(42))}
					if topic != nil {
						snapshot.Topic = &conversation.Topic{ID: *topic}
					}
					s.Messages = sidebarReader{page: snapshot}
					s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: quietLog{}, Authorizer: streamAllow(true), Sessions: s.Sessions,
						Owners: map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestTyping: NewTypingOwner(state)},
						StreamOpened: func(o, m, c kernel.ID) func() {
							closeStream := state.Open(o, m, c)
							return func() { closeStream(); closed <- m }
						}}
				})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					r.Header.Set("Accept-Language", lang)
					if strings.HasSuffix(r.URL.Path, "/events") {
						ctx, cancel := context.WithCancel(r.Context())
						defer cancel()
						requests <- connection{cancel, r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")}
						r = r.WithContext(ctx)
					}
					handler.ServeHTTP(w, r)
					if strings.HasSuffix(r.URL.Path, "/typing") {
						signaled <- struct{}{}
					}
				}))
				t.Cleanup(server.Close)
				page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
				t.Cleanup(func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
				page.MustEvalOnNewDocument(`document.addEventListener('htmx:sseOpen', e => window.testSource = e.detail.source);
 document.addEventListener('htmx:sseMessage', e => {
  if (e.target.id === 'typing-status') document.body.dataset.typingFrames = String(+(document.body.dataset.typingFrames || 0) + 1);
 });`)
				url := server.URL + view.ConversationURL(m.Organization.Slug, place.Channel, topic)
				page.MustNavigate(url).MustWaitLoad()
				page.MustWait(`() => +document.body.dataset.typingFrames > 0`)
				return screen{page, requests}
			}
			next := func(s screen) connection {
				t.Helper()
				select {
				case c := <-s.requests:
					if c.after != "42" || c.lastID != "" {
						t.Fatalf("typing changed reconnect cursor: %+v", c)
					}
					return c
				case <-ctx.Done():
					t.Fatal("stream did not connect", ctx.Err())
					return connection{}
				}
			}
			a, b := kernel.ID{3}, kernel.ID{4}
			typist, feed, topicA, topicB := open(8, &a), open(9, nil), open(9, &a), open(9, &b)
			next(typist)
			feedConnection := next(feed)
			next(topicA)
			next(topicB)
			extra := open(8, &a)
			next(extra)
			// Key and input actions wait for a repaint, which a background tab never does.
			feed.page.MustActivate()
			feed.page.MustElement("#message-body").MustType(input.Tab)
			if !feed.page.MustEval(`() => document.activeElement === document.querySelector('#message-composer button')`).Bool() {
				t.Fatal("typing introduced a keyboard stop")
			}
			feed.page.MustElement("#message-body").MustFocus()
			feed.page.MustEval(`() => window.statusBefore = document.getElementById('typing-status')`)
			signalInput := func() {
				typist.page.MustActivate()
				typist.page.MustElement("#message-body").MustInput("draft")
				select {
				case <-signaled:
				case <-ctx.Done():
					t.Fatal("input did not signal", ctx.Err())
				}
			}
			expect := func(s screen, want string) {
				t.Helper()
				// Compare the rendered text: template whitespace collapses on screen.
				s.page.MustWait(`want => document.getElementById('typing-status').textContent.replace(/\s+/g, ' ').trim() === want`, want)
			}
			one := "Alice @alice is typing…"
			if lang == "ja" {
				one = "Alice @aliceが入力中…"
			}
			signalInput()
			expect(feed, one)
			expect(topicA, one)
			reconnect := func(s screen) string {
				t.Helper()
				// Stale content survives the disconnect and must be replaced even
				// when this place has never had a nonzero generation.
				s.page.MustEval(`() => {
 document.getElementById('typing-status').textContent = 'stale';
 window.testSource.close();
 window.testSource.dispatchEvent(new Event('error'));
}`)
				next(s)
				s.page.MustWait(`() => document.getElementById('typing-status').textContent !== 'stale'`)
				return s.page.MustEval(`() => document.getElementById('typing-status').textContent.trim()`).String()
			}
			// Check the reconnect frame itself: waiting for empty would also pass
			// once the typist's activity expires.
			if got := reconnect(topicB); got != "" {
				t.Fatalf("topic B shows topic A's typing after reconnect: %q", got)
			}
			if got := reconnect(typist); got != "" {
				t.Fatalf("typist sees their own typing after reconnect: %q", got)
			}
			if !feed.page.MustEval(`() => {
 const status = document.getElementById('typing-status');
 return status === window.statusBefore && status.getAttribute('role') === 'status' &&
  status.getAttribute('aria-live') === 'polite' && status.getAttribute('aria-atomic') === 'true' &&
  document.activeElement.id === 'message-body' && !status.hasAttribute('tabindex') &&
  !document.querySelector('#message-list [aria-live]') && !document.getElementById('message-list').hasAttribute('aria-live') &&
  document.getElementById('organization-stream').dataset.eventCursor === '42';
}`).Bool() {
				t.Fatal("typing replacement moved focus, status or durable cursor")
			}
			expect(feed, "")
			expect(topicA, "")
			signalInput()
			expect(feed, one)
			navigated := typist.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
			typist.page.MustActivate()
			typist.page.MustElement("#message-body").MustType(input.Enter)
			navigated()
			if len(state.Read(kernel.ID{7}, typing.Place{Channel: kernel.ID{1}, Topic: a}, kernel.ID{9}).Typists) != 0 {
				t.Fatal("send did not clear typing")
			}
			expect(feed, "")
			expect(topicA, "")
			next(typist)
			typist.page.MustWait(`() => +document.body.dataset.typingFrames > 0`)
			signalInput()
			expect(feed, one)
			// Native retry restores current content; next checks its durable cursor.
			feed.page.MustEval(`() => document.getElementById('typing-status').textContent = 'stale'`)
			feedConnection.cancel()
			next(feed)
			expect(feed, one)
			signalInput() // Renew after the native retry before checking stream closes.
			closeOne := func(s screen) {
				t.Helper()
				s.page.MustEval(`() => window.testSource.close()`)
				for {
					select {
					case member := <-closed:
						if member == (kernel.ID{8}) {
							return
						}
					case <-ctx.Done():
						t.Fatal("stream did not close", ctx.Err())
					}
				}
			}
			// Drain completed reconnect cleanups before testing matching closes.
			for len(closed) > 0 {
				<-closed
			}
			closeOne(extra)
			if len(state.Read(kernel.ID{7}, typing.Place{Channel: kernel.ID{1}, Topic: a}, kernel.ID{9}).Typists) != 1 {
				t.Fatal("one of several streams cleared activity")
			}
			expect(feed, one)
			closeOne(typist)
			if len(state.Read(kernel.ID{7}, typing.Place{Channel: kernel.ID{1}, Topic: a}, kernel.ID{9}).Typists) != 0 {
				t.Fatal("last close did not clear typing")
			}
			expect(feed, "")
			expect(topicA, "")
			reconnect(feed)
			expect(feed, "")
			wants := map[string]map[int]string{
				"en": {1: "@Name1 is typing…", 3: "@Name3, @Name2 and @Name1 are typing…", 5: "@Name5, @Name4, @Name3 and 2 others are typing…"},
				"ja": {1: "@Name1が入力中…", 3: "@Name3、@Name2、@Name1が入力中…", 5: "@Name5、@Name4、@Name3、ほか2人が入力中…"},
			}
			for count := 1; count <= 5; count++ {
				id := kernel.ID{byte(20 + count)}
				t.Cleanup(state.Open(kernel.ID{7}, id, kernel.ID{1}))
				state.Start(kernel.ID{7}, typing.Place{Channel: kernel.ID{1}, Topic: a},
					typing.Typist{Member: id, Handle: fmt.Sprintf("Name%d", count)})
				if want := wants[lang][count]; want != "" {
					expect(feed, want)
					expect(topicA, want)
				}
			}
		})
	}
}
