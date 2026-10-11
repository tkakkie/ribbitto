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
	"github.com/tkakkie/ribbitto/internal/kernel"
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
