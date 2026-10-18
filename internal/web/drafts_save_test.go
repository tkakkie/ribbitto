package web

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestDraftComposerPlace(t *testing.T) {
	for _, topic := range []bool{false, true} {
		h, _, _, place := composerHandler(t, topic)
		path := view.ConversationURL("acme", place.Channel, nil)
		wantTopic := ""
		if topic {
			path = view.ConversationURL("acme", place.Channel, &place.Topic)
			wantTopic = "03000000-0000-0000-0000-000000000000"
		}
		for _, suffix := range []string{"", "?before=40", "?around=40"} {
			want := map[string]string{}
			if suffix == "" {
				want["data-draft-channel"] = "01000000-0000-0000-0000-000000000000"
				if topic {
					want["data-draft-topic"] = wantTopic
				}
			}
			w := serveForm(h, "GET", path+suffix, "live", nil)
			if w.Code != 200 {
				t.Fatalf("%s: %d", path+suffix, w.Code)
			}
			assertFullConversationPage(t, w.Body.String())
			doc, err := html.Parse(strings.NewReader(w.Body.String()))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for n := range doc.Descendants() {
				if attr(n, "id") != "message-composer" {
					continue
				}
				found = true
				got := map[string]string{}
				for _, a := range n.Attr {
					if a.Key == "data-draft-channel" || a.Key == "data-draft-topic" {
						got[a.Key] = a.Val
					}
				}
				if !maps.Equal(got, want) {
					t.Fatalf("%s: draft place %v, want %v", path+suffix, got, want)
				}
			}
			if !found {
				t.Fatal("missing composer")
			}
		}
	}
}

func testSaveDraftsBrowser(t *testing.T, browser *rod.Browser) {
	const session = "019a1b2c-3d4e-7800-8800-000000000000"
	login := &currentLogin{}
	login.Store(&draftsLogin{"08000000-0000-0000-0000-000000000000", session, time.Now().Add(30 * 24 * time.Hour)})
	signIn := &draftsSignIn{}
	horizon, _ := pathID("019a1b2c-3d4f-7000-8000-000000000000")
	signIn.horizon.Store(&horizon)
	// The cases share handlers and burst input without real sleeps. Give each
	// typing request a fresh allowance; rate limiting has its own tests.
	var signalTicks atomic.Int64
	signalNow := time.Now()
	setup := func(s *Services) {
		s.Sessions, s.SignIn = login, signIn
		s.SignalLimits = middleware.NewMemberRateLimiter(func() time.Time {
			return signalNow.Add(time.Duration(signalTicks.Add(1)) * time.Second)
		})
	}
	handler, _, _, place := composerHandler(t, true, setup)
	feedHandler, _, _, _ := composerHandler(t, false, setup)
	var noticeOnly, holdPosts atomic.Bool
	type postGate struct {
		done     chan struct{}
		requests chan pendingRequest
	}
	var gate atomic.Pointer[postGate]
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signin" {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		}
		if holdPosts.Load() && r.Method == "POST" && strings.Contains(r.URL.Path, "/channels/") && !strings.HasSuffix(r.URL.Path, "/typing") && !strings.HasSuffix(r.URL.Path, "/read") {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			request := pendingRequest{r.PostForm, make(chan browserReply, 1)}
			current := gate.Load()
			select {
			case current.requests <- request:
			case <-current.done:
				return
			case <-stop:
				return
			case <-r.Context().Done():
				return
			}
			select {
			case reply := <-request.reply:
				if reply.status == -1 {
					// An empty response can be retried by Chromium into the gate.
					// Headers followed by a truncated body fail the original XHR.
					w.Header().Set("Content-Length", "1")
					w.WriteHeader(http.StatusOK)
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error(err)
					}
					panic(http.ErrAbortHandler)
				} else {
					w.WriteHeader(reply.status)
					_, _ = w.Write(reply.body)
				}
			case <-current.done:
			case <-stop:
			case <-r.Context().Done():
			}
			return
		}
		if noticeOnly.Load() && r.URL.Path == "/signout" {
			// Even Chromium's response-header cleanup must not mask the
			// notice test: keep the real sign-out, suppress only its cleanup.
			rec := httptest.NewRecorder()
			feedHandler.ServeHTTP(rec, r)
			rec.Header().Del("Clear-Site-Data")
			for key, values := range rec.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		if strings.Contains(r.URL.Path, "/topics/") {
			handler.ServeHTTP(w, r)
		} else {
			feedHandler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stop) })
	step := func(t *testing.T, p *rod.Page, name string) *rod.Page {
		t.Helper()
		bounded := p.Timeout(3 * time.Second).WithPanic(func(v any) { t.Fatalf("%s: %v", name, v) })
		t.Cleanup(func() { bounded.CancelTimeout() })
		return bounded
	}
	eval := func(t *testing.T, p *rod.Page, name, js string, args ...any) *proto.RuntimeRemoteObject {
		t.Helper()
		result, err := step(t, p, name).Eval(js, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	open := func(t *testing.T, topic bool, inject string) *rod.Page {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		t.Cleanup(cancel)
		// Rod's page event loop inherits the creation context. Stop only the
		// creation timer on success, keeping that context alive for the page.
		creating, cancelCreating := context.WithCancel(ctx)
		t.Cleanup(cancelCreating)
		creationTimer := time.AfterFunc(3*time.Second, cancelCreating)
		p, err := browser.Context(creating).Page(proto.TargetCreateTarget{})
		creationTimer.Stop()
		if err != nil {
			t.Fatalf("create draft page: %v", err)
		}
		p = p.Context(ctx)
		t.Cleanup(func() { step(t, p.Context(context.Background()), "close draft page").MustClose() })
		// Only the drafts debounce is gated, including cancellation. Lifecycle
		// events and clocks are controlled independently of focus and real sleeps.
		step(t, p, "install draft controls").MustEvalOnNewDocument(`(() => {
   const timers = new Map(), set = window.setTimeout, clear = window.clearTimeout; let id = 0;
   window.setTimeout = (f, d, ...a) => { if (d > 60000) window.expireDrafts = () => f(...a); return d === 300 ? (timers.set(--id, {run: () => f(...a), left:d}), id) : set(f, d, ...a); };
   window.clearTimeout = id => { timers.delete(id); clear(id); };
   window.tickDrafts = ms => { for (const [id, task] of [...timers]) { task.left -= ms; if (task.left <= 0) { timers.delete(id); task.run(); } } };
   window.releaseDrafts = () => tickDrafts(300);
   window.pendingDrafts = () => timers.size;
   window.editNow = Date.now(); Date.now = () => editNow;
   const now = performance.now.bind(performance); window.elapsed = 0; performance.now = () => now() + elapsed;
   window.draftHidden = false; Object.defineProperty(document, 'hidden', {get: () => draftHidden});
   window.draftUUID = crypto.randomUUID?.bind(crypto); Object.defineProperty(crypto, 'randomUUID', {value: undefined, configurable:true});
   window.sendEvents = []; window.sentEvents = []; window.sendErrors = 0; window.restoreInputs = 0; window.typingRequests = 0;
   document.addEventListener('message-sending', e => sendEvents.push(e.detail));
   document.addEventListener('message-sent', e => sentEvents.push(e.detail));
   document.addEventListener('htmx:sendError', () => sendErrors++);
   document.addEventListener('input', e => { if (e.target.id === 'message-body') restoreInputs++; });
   document.addEventListener('htmx:beforeRequest', e => { if (e.target.id === 'typing-form') typingRequests++; });
   document.addEventListener('readystatechange', () => { if(document.readyState === 'interactive') window.restoreFocus = document.activeElement; });
   window.swaps = 0; window.signals = 0;
   document.addEventListener('htmx:afterSwap', e => { if (e.target.id === 'message-composer-state') window.swapStatus = e.detail.xhr.status; });
   document.addEventListener('htmx:afterSettle', e => { if (e.target.id === 'message-composer-state') swaps++; });
   document.addEventListener('htmx:afterRequest', e => { if (e.detail.elt.id === 'typing-form' && e.detail.xhr.status === 204) signals++; });
   window.check = (ok, why) => { if (!ok) throw new Error(why); };
   window.edit = (text, at) => { editNow = at; const b = document.querySelector('#message-body'); b.value = text; b.dispatchEvent(new Event('input', {bubbles:true})); };
   window.flush = () => { draftHidden = true; document.dispatchEvent(new Event('visibilitychange')); draftHidden = false; };
   window.hide = () => dispatchEvent(new Event('pagehide'));
   window.scope = () => { const f=document.querySelector('#message-composer').dataset, p=document.querySelector('#organization-stream').dataset; return 'ribbitto.draft.v1:'+p.member+':'+p.session+':'+f.draftChannel+':'+(f.draftTopic || 'feed'); };
   window.entry = () => JSON.parse(localStorage.getItem(scope()));
   window.snapshot = () => JSON.stringify(Object.entries(localStorage).sort());
  })();
` + inject)
		path := view.ChannelURL("acme", place.Channel)
		if topic {
			path = view.ConversationURL("acme", place.Channel, &place.Topic)
		}
		step(t, p, "navigate to composer").MustNavigate(server.URL + path)
		step(t, p, "load composer").MustWaitLoad()
		step(t, p, "await composer textarea").MustElement("#message-body")
		eval(t, p, "read draft scope", `() => { const f = document.querySelector('#message-composer').dataset, p = document.querySelector('#organization-stream').dataset;
   window.draftKey = 'ribbitto.draft.v1:' + p.member + ':' + p.session + ':' + f.draftChannel + ':' + (f.draftTopic || 'feed'); }`)
		if holdPosts.Load() {
			current := &postGate{make(chan struct{}), make(chan pendingRequest, 8)}
			gate.Store(current)
			t.Cleanup(func() { close(current.done) })
		}
		return p
	}
	run := func(t *testing.T, p *rod.Page, code string) {
		t.Helper()
		eval(t, p, "draft assertions: "+strings.SplitN(code, "\n", 2)[0], `() => { `+code+` }`)
	}
	// Sequential CDP evaluations do not order storage propagation to another
	// tab. A unique marker also orders a remove/restore whose final snapshot
	// equals the reader's old one; waiting for that snapshot alone could pass early.
	storageSync := 0
	syncStorage := func(t *testing.T, from, to *rod.Page) {
		t.Helper()
		const marker = "ribbitto.test-storage-barrier"
		storageSync++
		want := eval(t, from, "write storage barrier", `(key, value) => { localStorage.setItem(key, value); return snapshot(); }`, marker, storageSync).Value.Str()
		step(t, to, "propagate storage snapshot").MustWait(`want => snapshot() === want`, want)
		eval(t, from, "remove storage barrier", `key => localStorage.removeItem(key)`, marker)
		step(t, to, "propagate barrier removal").MustWait(`key => localStorage.getItem(key) === null`, marker)
	}

	reload := func(t *testing.T, p *rod.Page, want string) {
		t.Helper()
		step(t, p, "reload composer").MustReload()
		// Poll the current document instead of awaiting a load promise in a
		// context the reload can replace (as in navigateLoaded).
		step(t, p, "load reloaded composer").MustWait(`() => document.readyState === 'complete'`)
		step(t, p, "await restored textarea").MustWait(`want => document.querySelector('#message-body')?.value === want`, want)
		run(t, p, `check(restoreInputs === 0 && typingRequests === 0 && document.activeElement === restoreFocus, 'restore fired input, typing or focus');`)
	}
	for _, mode := range []string{"reload", "switch", "compositionend", "pagehide", "plain-422", "deadline"} {
		t.Run("restore/"+mode, func(t *testing.T) {
			p := open(t, false, "")
			run(t, p, `localStorage.clear(); edit('feed draft', editNow);`)
			switch mode {
			case "compositionend":
				run(t, p, `const b=document.querySelector('#message-body'); b.dispatchEvent(new Event('compositionstart')); edit('日本', ++editNow); b.dispatchEvent(new Event('compositionend',{bubbles:true})); releaseDrafts();`)
			case "pagehide":
				run(t, p, `check(pendingDrafts()===1,'missing pending pagehide save'); hide(); check(entry().text==='feed draft','pagehide did not flush before pause');`)
			default:
				run(t, p, `releaseDrafts();`)
			}
			switch mode {
			case "switch":
				step(t, p, "switch to topic").MustNavigate(server.URL + view.ConversationURL("acme", place.Channel, &place.Topic))
				step(t, p, "load topic").MustWaitLoad()
				step(t, p, "await separate empty topic draft").MustWait(`() => document.querySelector('#message-body')?.value === ''`)
				run(t, p, `edit('topic draft', ++editNow); releaseDrafts();`)
				step(t, p, "switch back to feed").MustNavigate(server.URL + view.ChannelURL("acme", place.Channel))
				step(t, p, "load feed").MustWaitLoad()
				reload(t, p, "feed draft")
				topic := open(t, true, "")
				step(t, topic, "await separate restored topic draft").MustWait(`() => document.querySelector('#message-body')?.value === 'topic draft'`)
			case "plain-422":
				run(t, p, `const f=document.querySelector('#message-composer'); f.removeAttribute('hx-post'); document.querySelector('#message-body').value='invalid\u0001body';`)
				nav := step(t, p, "submit plain invalid body")
				wait := nav.WaitNavigation(proto.PageLifecycleEventNameLoad)
				nav.MustEval(`() => document.querySelector('#message-composer').submit()`)
				wait()
				step(t, p, "await server-rendered body saved over draft").MustWait(`() => document.querySelector('#message-body')?.value === 'invalid\u0001body' && entry()?.text === 'invalid\u0001body'`)
			case "deadline":
				step(t, p, "install expired load clock").MustEvalOnNewDocument(`document.addEventListener('readystatechange', () => { if(document.readyState === 'interactive') elapsed=31*86400000; });`)
				reload(t, p, "")
			case "compositionend":
				reload(t, p, "日本")
			default:
				reload(t, p, "feed draft")
			}
		})
	}
	// The server gate outlives navigation; each acquired request is released
	// before page cleanup even on assertion failure. No CDP route owns a POST.
	nextPost := func(t *testing.T) (pendingRequest, func(browserReply)) {
		t.Helper()
		select {
		case req := <-gate.Load().requests:
			var once sync.Once
			release := func(reply browserReply) { once.Do(func() { req.reply <- reply }) }
			t.Cleanup(func() { release(browserReply{status: 500}) })
			return req, release
		case <-time.After(3 * time.Second):
			t.Fatal("await held composer POST")
			return pendingRequest{}, nil
		}
	}
	for _, pending := range []bool{false, true} {
		for _, mode := range []string{"success", "422", "404", "429", "500", "network", "reload-held", "typing-success", "typing-failure", "tab-write", "composition"} {
			t.Run(fmt.Sprintf("results/pending=%t/%s", pending, mode), func(t *testing.T) {
				holdPosts.Store(true)
				defer holdPosts.Store(false)
				inject := ""
				if pending {
					inject = `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`
				}
				p := open(t, false, inject)
				run(t, p, `localStorage.clear(); edit('A', editNow); releaseDrafts(); edit('B', ++editNow); document.querySelector('#message-composer').requestSubmit(); check(entry().text === 'B' && pendingDrafts() === 0 && sendEvents.length === 1 && sendEvents[0].body === 'B', 'send snapshot/event');`)
				req, release := nextPost(t)
				eval(t, p, "check send identity and path", `(key, pending) => check(sendEvents[0].key === key && sendEvents[0].inPlace === !pending, 'send key/path')`, req.values.Get("idempotency_key"), pending)
				want, status := "", 200
				switch mode {
				case "422":
					status = 422
				case "404":
					status = 404
				case "429":
					status = 429
				case "500", "typing-failure":
					status = 500
				case "network":
					status = -1
				}
				if status != 200 {
					want = "B"
				}
				if strings.HasPrefix(mode, "typing-") || mode == "composition" {
					if mode == "composition" {
						run(t, p, `document.querySelector('#message-body').dispatchEvent(new Event('compositionstart',{bubbles:true}));`)
					}
					eval(t, p, "type during held send", `pending => edit(pending ? 'C' : 'BC', ++editNow)`, pending)
					if mode != "composition" {
						run(t, p, `releaseDrafts();`)
					}
					want = "C"
					if status != 200 && !pending {
						want = "BC"
					}
				}
				if mode == "tab-write" {
					other := open(t, false, "")
					run(t, other, `edit('D', editNow+10000); releaseDrafts();`)
					syncStorage(t, other, p)
					want = "D"
				}
				if mode == "reload-held" {
					reload(t, p, "B")
					release(browserReply{status: 500})
					return
				}
				reply := pendingAnswer(t, place.Channel)
				if status != 200 {
					reply = browserReply{status: status}
					if status == 422 {
						reply.body = []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{})))
					}
				}
				release(reply)
				if mode == "network" {
					step(t, p, "await send network error").MustWait(`() => sendErrors === 1`)
				}
				step(t, p, "await send request completion").MustWait(`() => !document.querySelector('#message-composer.htmx-request') && !document.querySelector('[data-pending-form].htmx-request')`)
				if mode == "composition" {
					run(t, p, `check(sentEvents.length === 0 || sendEvents[0].inPlace === false, 'sent before composition removal'); document.querySelector('#message-body').dispatchEvent(new Event('compositionend',{bubbles:true})); releaseDrafts();`)
				}
				if status == 200 {
					step(t, p, "await confirmation event").MustWait(`() => sentEvents.length === 1 && sentEvents[0].key === sendEvents[0].key && !!sentEvents[0].message`)
				} else {
					run(t, p, `check(sentEvents.length === 0, 'failure emitted success');`)
				}
				// Pending composition intentionally holds its own edit until compositionend.
				run(t, p, `releaseDrafts();`)
				step(t, p, "await result draft value").MustWait(`want => entry()?.text === want`, want)
				reload(t, p, want)
			})
		}
	}
	for _, mode := range []string{"success", "failure", "reload-held", "save-pending", "unedited"} {
		t.Run("retry/"+mode, func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := open(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `localStorage.clear(); edit('A',editNow); document.querySelector('#message-composer').requestSubmit();`)
			first, release := nextPost(t)
			release(browserReply{status: 500})
			step(t, p, "await failed pending item").MustWait(`() => document.querySelector('[data-pending-form]')?.dataset.state === 'failed' && !document.querySelector('[data-pending-form].htmx-request')`)
			want := "C"
			if mode == "unedited" {
				want = ""
			} else {
				run(t, p, `edit('C',++editNow);`)
				if mode != "save-pending" {
					run(t, p, `releaseDrafts();`)
				}
			}
			step(t, p, "submit pending Retry").MustEval(`() => document.querySelector('[data-pending-form]').requestSubmit()`)
			retry, release := nextPost(t)
			if retry.values.Get("idempotency_key") != first.values.Get("idempotency_key") {
				t.Fatal("Retry changed send identity")
			}
			run(t, p, `check(sendEvents.length === 1, 'Retry emitted message-sending'); releaseDrafts();`)
			if mode == "reload-held" {
				reload(t, p, want)
				release(browserReply{status: 500})
				return
			}
			reply := pendingAnswer(t, place.Channel)
			if mode == "failure" {
				reply = browserReply{status: 500}
			}
			release(reply)
			step(t, p, "await Retry outcome").MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
			step(t, p, "await Retry draft and textarea").MustWait(`want => entry()?.text === want && document.querySelector('#message-body').value === want`, want)
			reload(t, p, want)
		})
	}

	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue/success=%t", success), func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := open(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `localStorage.clear(); edit('A',editNow); document.querySelector('#message-composer').requestSubmit();`)
			first, releaseA := nextPost(t)
			run(t, p, `edit('B',++editNow); document.querySelector('#message-composer').requestSubmit(); check(entry().text==='B','queued B snapshot');`)
			releaseA(pendingAnswer(t, place.Channel))
			second, releaseB := nextPost(t)
			if first.values.Get("idempotency_key") == second.values.Get("idempotency_key") {
				t.Fatal("queue reused send identity")
			}
			step(t, p, "await A confirmation retaining B").MustWait(`() => sentEvents.length===1 && sentEvents[0].key===sendEvents[0].key && entry()?.text==='B'`)
			run(t, p, `document.querySelector('#message-body').dispatchEvent(new CustomEvent('message-sent',{bubbles:true,detail:{key:'unknown',message:'x'}})); check(entry().text==='B','unknown key cleared B');`)
			copy := open(t, false, "")
			reload(t, copy, "B")
			want, reply := "B", browserReply{status: 500}
			if success {
				want = ""
				reply = pendingAnswer(t, place.Channel)
			}
			releaseB(reply)
			step(t, p, "await B result").MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
			reload(t, p, want)
		})
	}
	t.Run("saving", func(t *testing.T) {
		p := open(t, false, "")
		run(t, p, `localStorage.clear(); hide(); flush(); check(!entry(), 'clean page wrote');
   const at = editNow; edit('first', at - 1); tickDrafts(299); check(!entry(), 'early save');
   edit('  🐸\n', at); tickDrafts(299); check(!entry(), 'debounce did not restart'); editNow += 50; tickDrafts(1);
   const e = entry(), page = document.querySelector('#organization-stream').dataset;
   check(e.text === '  🐸\n' && e.at === at && e.member === page.member && e.session === page.session && e.expires === +page.sessionExpires, 'value or input time');
   edit('next', at - 1); hide(); check(entry().at === at + 1, 'clock rollback');
   edit('same millisecond', at - 1); flush(); check(entry().at === at + 2, 'repeated clock time');
   edit('before composition', at + 50); check(pendingDrafts() === 1, 'missing pending save');
   const b = document.querySelector('#message-body'); b.dispatchEvent(new Event('compositionstart')); check(pendingDrafts() === 0, 'composition did not cancel save'); edit('日本', at + 100);
   releaseDrafts(); flush(); hide(); check(entry().text === 'same millisecond', 'saved composition');
   b.dispatchEvent(new Event('compositionend')); releaseDrafts(); check(entry().text === '日本', 'composition end');
   for (const [text, want] of [['🐸'.repeat(4000), '🐸'.repeat(4000)], ['🐸'.repeat(4001), ''], [' \n\t', '']]) {
    edit(text, ++editNow); flush(); check(entry().text === want, 'bound/tombstone');
   }
   check(b.value === ' \n\t', 'changed textarea');`)
	})
	for _, mode := range []string{"delayed", "deletion", "over-limit", "equal", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			a, b := open(t, false, ""), open(t, false, "")
			run(t, a, `localStorage.clear(); edit('A', editNow + 100);`)
			syncStorage(t, a, b)
			eval(t, b, "save competing draft", `(mode, aAt) => { edit('B', mode === 'equal' ? aAt : editNow + 1000); flush();
    check(entry().text === 'B' && (mode !== 'equal' || entry().at === aAt), 'B save or equal input time');
    if (mode === 'deletion' || mode === 'over-limit') { edit(mode === 'deletion' ? ' ' : '🐸'.repeat(4001), editNow + 1); flush(); } }`, mode, eval(t, a, "read input time", `() => editNow`).Value.Num())
			syncStorage(t, b, a)
			run(t, a, `const winner = localStorage.getItem(draftKey); releaseDrafts(); check(localStorage.getItem(draftKey) === winner, 'older/equal save won');
    localStorage.removeItem(draftKey); hide(); flush(); check(!entry(), 'skipped save stayed dirty'); localStorage.setItem(draftKey, winner);`)
			syncStorage(t, a, b)
			if mode == "overlap" {
				// Both reads could have preceded either write: apply A's stale write.
				eval(t, b, "apply overlapping save", `at => { const older = {...entry(), text:'A', at}; localStorage.setItem(draftKey, JSON.stringify(older));
     hide(); flush(); check(entry().text === 'A' && document.querySelector('#message-body').value === 'B', 'clean tab repaired overlap');
     edit('B again', editNow + 1); releaseDrafts(); check(entry().text === 'B again', 'next input did not recover'); }`, eval(t, a, "read input time", `() => editNow`).Value.Num())
			} else {
				eval(t, b, "check stored winner", `mode => check(entry().text === (mode === 'deletion' || mode === 'over-limit' ? '' : 'B'), 'stored winner')`, mode)
			}
		})
	}
	for _, order := range []bool{false, true} {
		t.Run(fmt.Sprintf("cap/reverse=%t", order), func(t *testing.T) {
			a, b := open(t, false, ""), open(t, true, "")
			run(t, a, `localStorage.clear(); edit('A', editNow + 100); flush(); const e = entry(); localStorage.clear();
    for (let n=1; n<=49; n++) localStorage.setItem(draftKey.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), JSON.stringify({...e, text:'', at:e.at - 1000}));
    edit('A', editNow + 1);`)
			syncStorage(t, a, b)
			run(t, b, `edit('B', editNow + 1000);`)
			for _, p := range []*rod.Page{a, b} {
				run(t, p, `const set = Storage.prototype.setItem; Storage.prototype.setItem = function(k, v) {
     check(k !== draftKey || localStorage.getItem(k) !== null || Object.keys(localStorage).length < 50, 'cap exceeded at insertion'); return set.call(this, k, v); };`)
			}
			first, second := a, b
			if order {
				first, second = b, a
			}
			run(t, first, `releaseDrafts(); check(Object.keys(localStorage).length === 50, 'first cap');`)
			syncStorage(t, first, second)
			run(t, second, `releaseDrafts(); check(Object.keys(localStorage).length === 50 && entry().text, 'second cap');
    check(!Object.keys(localStorage).some(k => k.endsWith('00000000-0000-0000-0000-000000000001')), 'oldest tie');`)
			syncStorage(t, second, first)
			run(t, first, `check(entry().text, 'other new entry evicted');`)
		})
	}
	t.Run("trim after write", func(t *testing.T) {
		p := open(t, false, "")
		run(t, p, `localStorage.clear(); edit('seed', editNow); flush(); const e = entry(); localStorage.clear();
   const oldest = draftKey.replace(/feed$/, 'ffffffff-ffff-ffff-ffff-ffffffffffff');
   localStorage.setItem(oldest, JSON.stringify({...e, at:editNow - 1000}));
   for (let n=1; n<49; n++) localStorage.setItem(draftKey.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), JSON.stringify(e));
   // A second tab can insert after the pre-write trim observed 49 entries.
   const set = Storage.prototype.setItem;
   Storage.prototype.setItem = function(k, v) { set.call(this, k, v); if (k === draftKey) set.call(this, k.replace(/feed$/, 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee'), v); };
   edit('new', editNow + 1); releaseDrafts();
   check(Object.keys(localStorage).length === 50 && !localStorage.getItem(oldest), 'post-write trim or edit-time order');`)
	})
	t.Run("update at cap", func(t *testing.T) {
		p := open(t, false, "")
		run(t, p, `localStorage.clear(); edit('own', editNow); flush(); const e = entry();
   for (let n=1; n<50; n++) localStorage.setItem(draftKey.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), JSON.stringify({...e, at:editNow - n}));
   edit('updated', editNow + 1); releaseDrafts(); check(entry().text === 'updated' && Object.keys(localStorage).length === 50, 'update evicted another place');`)
	})
	// Each foreign set differs in just one owner field: neither predicate
	// can accidentally be covered by the other one excluding the same keys.
	for _, field := range []string{"member", "session"} {
		t.Run("scope/"+field, func(t *testing.T) {
			p := open(t, false, "")
			eval(t, p, "check foreign draft scope", `field => { localStorage.clear(); edit('seed', editNow); flush(); const e = entry(); localStorage.clear();
    const foreign = draftKey.replace(e[field], field === 'member' ? '09000000-0000-0000-0000-000000000000' : '019a1b2c-3d51-7000-8000-000000000000');
    for (let n=1; n<=50; n++) localStorage.setItem(foreign.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), 'foreign unread');
    const before = snapshot(), reads = [], writes = [], get = Storage.prototype.getItem, set = Storage.prototype.setItem;
    Storage.prototype.getItem = function(k) { reads.push(k); return get.call(this, k); };
    Storage.prototype.setItem = function(k, v) { writes.push(k); return set.call(this, k, v); };
    edit('own', editNow + 1); releaseDrafts();
    check(entry().text === 'own' && reads.every(k => k === draftKey), 'foreign value read');
    check(writes.length === 1 && writes[0] === draftKey, 'foreign key written');
    localStorage.removeItem(draftKey); check(snapshot() === before, 'foreign entry evicted'); }`, field)
		})
	}
	for _, guard := range []string{"hx-post", "place"} {
		t.Run("scope/"+guard, func(t *testing.T) {
			// Interactive precedes deferred scripts: remove only this guard's
			// input while leaving every other eligibility condition valid.
			inject := `document.addEventListener('readystatechange', () => { if (document.readyState === 'interactive') { const f = document.querySelector('#message-composer'); `
			if guard == "hx-post" {
				inject += `f.removeAttribute('hx-post');`
			} else {
				inject += `f.dataset.draftChannel = 'invalid';`
			}
			p := open(t, false, inject+` } });`)
			run(t, p, `localStorage.clear(); edit('ineligible', editNow); releaseDrafts(); flush(); hide(); check(snapshot() === '[]', 'ineligible composer saved');`)
		})
	}
	t.Run("eviction overlap", func(t *testing.T) {
		a, b := open(t, true, ""), open(t, false, "")
		run(t, b, `localStorage.clear(); edit('oldest', editNow); flush(); window.oldest = draftKey;
   for (let n=1; n<50; n++) localStorage.setItem(draftKey.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), JSON.stringify({...entry(), at:editNow + n}));`)
		// A selected K before B updated it; apply A's removal and insertion.
		k := eval(t, b, "read eviction key", `() => oldest`).Value.Str()
		run(t, b, `edit('B newer', editNow + 1000); flush();`)
		syncStorage(t, b, a)
		eval(t, a, "apply overlapping eviction", `k => { localStorage.removeItem(k); edit('A', editNow + 2000); flush(); check(!localStorage.getItem(k) && Object.keys(localStorage).length === 50, 'eviction race'); }`, k)
		syncStorage(t, a, b)
		run(t, b, `hide(); check(!entry() && document.querySelector('#message-body').value === 'B newer', 'clean evicted tab wrote');
   edit('B recovered', editNow + 1); releaseDrafts(); check(entry().text === 'B recovered' && Object.keys(localStorage).length === 50, 'eviction recovery');`)
	})
	t.Run("sign-out stops before cleanup", func(t *testing.T) {
		noticeOnly.Store(true)
		defer noticeOnly.Store(false)
		a, b := open(t, false, ""), open(t, false, "")
		run(t, b, `localStorage.clear(); edit('saved', editNow); flush(); edit('pending', editNow + 1);
   window.notified = false; addEventListener('storage', e => { if (e.key === 'ribbitto.signed-out.v1') notified = true; });`)
		// Replace only the sign-in document with an inert one. Deliver its
		// notice explicitly, separating stopped writes from the cleanup script.
		router := a.HijackRequests()
		router.MustAdd("*/signin", func(h *rod.Hijack) {
			h.Response.SetHeader("Content-Type", "text/html").SetBody("<!doctype html><title>Signed out</title>")
		})
		go router.Run()
		defer router.MustStop()
		signOut := step(t, a, "submit sign-out and load sign-in")
		wait := signOut.WaitNavigation(proto.PageLifecycleEventNameLoad)
		signOut.MustElement(`form[action="/signout"]`).MustEval(`function() { this.requestSubmit(); }`)
		wait()
		if err := signOut.GetContext().Err(); err != nil {
			t.Fatalf("load sign-in after sign-out: %v", err)
		}
		eval(t, a, "publish sign-out notice", `h => localStorage.setItem('ribbitto.signed-out.v1', h)`, "019a1b2c-3d4f-7000-8000-000000000000")
		step(t, b, "receive sign-out notice").MustWait(`() => notified`)
		run(t, b, `check(entry()?.text === 'saved', 'cleanup ran before stop assertion');
   check(pendingDrafts() === 0, 'notice did not cancel save'); const before = snapshot();
   releaseDrafts(); edit('after sign-out', editNow + 1); flush(); hide(); check(snapshot() === before && pendingDrafts() === 0, 'ended session wrote');`)
	})
	t.Run("sign-out cleanup", func(t *testing.T) {
		p, other := open(t, false, ""), open(t, false, "")
		run(t, other, `localStorage.clear(); edit('ended session', editNow); flush(); edit('pending at sign-out', ++editNow); window.notified=false; addEventListener('storage',e=>{if(e.key==='ribbitto.signed-out.v1') notified=true;});`)
		signOut := step(t, p, "submit sign-out and load sign-in")
		wait := signOut.WaitNavigation(proto.PageLifecycleEventNameLoad)
		signOut.MustElement(`form[action="/signout"]`).MustEval(`function() { this.requestSubmit(); }`)
		wait()
		if err := signOut.GetContext().Err(); err != nil {
			t.Fatalf("load sign-in after sign-out: %v", err)
		}
		if !strings.HasSuffix(step(t, p, "read sign-out URL").MustInfo().URL, "/signin") {
			t.Fatal("sign-out did not reach sign-in")
		}
		run(t, p, `check(!Object.keys(localStorage).some(k => k.startsWith('ribbitto.draft.v1:')), 'sign-out cleanup');`)
		step(t, other, "await sign-out notice in pending tab").MustWait(`() => notified`)
		run(t, other, `releaseDrafts(); hide(); check(pendingDrafts()===0, 'sign-out left save pending');`)
		previous := login.Load()
		later := *previous
		later.session = "019a1b2c-3d50-7000-8000-000000000000"
		login.Store(&later)
		defer login.Store(previous)
		fresh := open(t, false, "")
		reload(t, fresh, "")
	})
	for _, oldFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("later session interleaving/old-first=%t", oldFirst), func(t *testing.T) {
			a := open(t, false, "")
			run(t, a, `localStorage.clear(); edit('pending old session', editNow + 100);`)
			if oldFirst {
				run(t, a, `releaseDrafts();`)
			}
			next := *login.Load()
			next.session = "019a1b2c-3d50-7000-8000-000000000000"
			login.Store(&next)
			t.Cleanup(func() {
				login.Store(&draftsLogin{"08000000-0000-0000-0000-000000000000", session, time.Now().Add(30 * 24 * time.Hour)})
			})
			b := open(t, false, "")
			step(t, b, "await empty later-session composer").MustWait(`() => document.querySelector('#message-body')?.value === ''`)
			run(t, a, `edit('pending old session', ++editNow); releaseDrafts(); check(entry().text === 'pending old session', 'stale write missing');`)
			syncStorage(t, a, b)
			step(t, b, "reload later session").MustNavigate(step(t, b, "read later-session URL").MustInfo().URL)
			step(t, b, "load later session").MustWaitLoad()
			run(t, b, `check(!Object.keys(localStorage).some(k => k.includes('`+session+`')) && document.querySelector('#message-body').value === '', 'later load restored stale entry');`)
		})
	}
	t.Run("expiry", func(t *testing.T) {
		p := open(t, false, "")
		run(t, p, `localStorage.clear(); edit('saved', editNow); flush(); edit('pending', editNow + 1); elapsed = 31*86400000; expireDrafts();
   check(!entry() && pendingDrafts() === 0, 'expiry cleanup/cancellation'); const before = snapshot();
   releaseDrafts(); edit('expired', editNow + 1); flush(); hide(); check(snapshot() === before, 'expiry wrote');
   elapsed = 0; edit('clock reset after deadline', editNow + 1); releaseDrafts(); check(snapshot() === before, 'expiry did not permanently stop');`)
	})
	for _, trigger := range []string{"input", "save"} {
		t.Run("deadline guard/"+trigger, func(t *testing.T) {
			p := open(t, false, "")
			eval(t, p, "check deadline guard", `trigger => { localStorage.clear(); if (trigger === 'save') edit('pending', editNow);
    elapsed = 31*86400000; if (trigger === 'input') edit('past deadline', editNow);
    if (trigger === 'input') check(pendingDrafts() === 0, 'expired input queued a save');
    // No deadline timer callback: queued lifecycle work still checks time.
    releaseDrafts(); flush(); hide(); check(!entry() && pendingDrafts() === 0, 'deadline guard failed');
    elapsed = 0; edit('after guard', editNow + 1); releaseDrafts(); check(!entry(), 'guard did not stop permanently'); }`, trigger)
		})
	}
	for _, mode := range []string{"feed", "topic", "quota", "blocked"} {
		t.Run("sending/"+mode, func(t *testing.T) {
			inject := `window.storageFailures = 0;`
			switch mode {
			case "quota":
				inject += `Object.defineProperty(Storage.prototype, 'setItem', {value: () => { storageFailures++; throw new Error('quota'); }});`
			case "blocked":
				inject += `Object.defineProperty(window, 'localStorage', {get: () => { storageFailures++; throw new Error('blocked'); }});`
			}
			p := open(t, mode != "feed", inject)
			run(t, p, `edit('still on page', editNow); releaseDrafts(); flush(); check(document.querySelector('#message-body').value === 'still on page', 'storage lost textarea');`)
			if mode == "quota" || mode == "blocked" {
				run(t, p, `check(storageFailures > 0, 'storage failure was not exercised');`)
			}
			step(t, p, "receive successful typing response").MustWait(`() => signals > 0`)
			for _, text := range []string{" ", "sent"} {
				status, topic := 200, "03000000-0000-0000-0000-000000000000"
				if text == " " {
					status = 422
				}
				if mode == "feed" {
					topic = ""
				}
				eval(t, p, fmt.Sprintf("submit body %q", text), `text => { document.querySelector('#message-body').value = text; document.querySelector('#message-composer').requestSubmit(); }`, text)
				step(t, p, fmt.Sprintf("settle %d response and enable send", status)).MustWait(`() => swaps > 0 && !document.querySelector('#message-composer button').disabled`)
				eval(t, p, fmt.Sprintf("check %d response scope", status), `(status, topic) => { const f = document.querySelector('#message-composer'); check(swapStatus === status && f.dataset.draftChannel === '01000000-0000-0000-0000-000000000000' && (f.dataset.draftTopic || '') === topic, 'swap status or place'); swaps = 0; }`, status, topic)
			}
			run(t, p, `check(document.querySelector('#message-body').value === '', 'sending broken');`)
		})
	}
}
