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
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
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
		s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) {
			l.signal = middleware.NewMemberRateLimiter(func() time.Time {
				return signalNow.Add(time.Duration(signalTicks.Add(1)) * time.Second)
			})
		})
	}
	handler, _, _, place := composerHandler(t, true, setup)
	feedHandler, _, _, _ := composerHandler(t, false, setup)
	otherHandler, _, _, _ := composerHandler(t, false, setup, func(s *Services) {
		s.Messages = sidebarReader{page: conversation.ChannelPage{Current: conversation.Channel{ID: kernel.ID{9}, DefaultTopicID: kernel.ID{2}}, EventCursor: new(int64(42))}}
	})
	// The page's member comes from the membership, not the login: serve the
	// other-member case from a handler whose membership is another member.
	otherMemberHandler, _, _, _ := composerHandler(t, false, setup, func(s *Services) {
		s.Authz = composerMember{membership: org.Membership{Organization: org.Organization{ID: kernel.ID{7}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{9}, Handle: "bob"}}}
	})
	var noticeOnly, holdPosts, otherMember atomic.Bool
	type postGate struct {
		done     chan struct{}
		requests chan pendingRequest
		stored   sync.Map
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
			// Model the server's same-key lookup before releasing its answer.
			current.stored.LoadOrStore(r.PostForm.Get("idempotency_key"), r.PostForm.Get("body"))
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
		if otherMember.Load() {
			otherMemberHandler.ServeHTTP(w, r)
		} else if strings.Contains(r.URL.Path, "/channels/09000000-") {
			otherHandler.ServeHTTP(w, r)
		} else if strings.Contains(r.URL.Path, "/topics/") {
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
	waitState := func(t *testing.T, p *rod.Page, name, condition, diagnostic string) {
		t.Helper()
		if err := step(t, p, name).Wait(rod.Eval(condition)); err != nil {
			diag := p.Context(context.Background()).Timeout(time.Second)
			defer diag.CancelTimeout()
			state, diagnosticErr := diag.Eval(diagnostic)
			if diagnosticErr != nil {
				t.Fatalf("%s: %v; state unavailable: %v", name, err, diagnosticErr)
			}
			t.Fatalf("%s: %v; state: %s", name, err, state.Value.Str())
		}
	}
	closed := make(map[proto.TargetTargetID]bool)
	closePage := func(t *testing.T, p *rod.Page) {
		t.Helper()
		if !closed[p.TargetID] {
			step(t, p.Context(context.Background()), "close draft page").MustClose()
			closed[p.TargetID] = true
		}
	}
	openPage := func(t *testing.T, topic bool, inject string, clean bool) *rod.Page {
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
		t.Cleanup(func() { closePage(t, p) })
		// Draft debounce and Remove expiry are gated, including cancellation. Lifecycle
		// events and clocks are controlled independently of focus and real sleeps.
		step(t, p, "install draft controls").MustEvalOnNewDocument(`(() => {
   const timers = new Map(), removedTimers = new Map(), set = window.setTimeout, clear = window.clearTimeout; let id = 0;
   window.setTimeout = (f, d, ...a) => {
    const gated = d === 300 ? timers : d === 300000 ? removedTimers : null;
    if (gated) { gated.set(--id, {run: () => f(...a), left:d}); return id; }
    if (d > 60000) window.expireDrafts = () => f(...a);
    return set(f, d, ...a);
   };
   window.clearTimeout = id => { timers.delete(id); removedTimers.delete(id); clear(id); };
   window.expireRemoved = () => { check(removedTimers.size === 1, 'missing Remove expiry timer'); for (const [id, task] of [...removedTimers]) { removedTimers.delete(id); task.run(); } };
   window.tickDrafts = ms => { for (const [id, task] of [...timers]) { task.left -= ms; if (task.left <= 0) { timers.delete(id); task.run(); } } };
   window.releaseDrafts = () => tickDrafts(300);
   window.pendingDrafts = () => timers.size;
   window.editNow = Date.now(); Date.now = () => editNow;
   const now = performance.now.bind(performance); window.elapsed = 0; performance.now = () => now() + elapsed;
   window.draftHidden = false; Object.defineProperty(document, 'hidden', {get: () => draftHidden});
   window.draftUUID = crypto.randomUUID?.bind(crypto); Object.defineProperty(crypto, 'randomUUID', {value: undefined, configurable:true});
   window.sendEvents = []; window.sentEvents = []; window.sendErrors = 0; window.restoreInputs = 0; window.typingRequests = 0;
   window.compositionActive = false;
   document.addEventListener('compositionstart', () => compositionActive = true, true);
   document.addEventListener('compositionend', () => compositionActive = false, true);
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
		// Remove this setup after the first load so reload still tests restore.
		var clearScript *proto.PageAddScriptToEvaluateOnNewDocumentResult
		if clean {
			clearScript, err = (proto.PageAddScriptToEvaluateOnNewDocument{Source: `localStorage.clear();`}).Call(step(t, p, "isolate draft storage before scripts"))
			if err != nil {
				t.Fatalf("install draft storage isolation: %v", err)
			}
		}
		path := view.ChannelURL("acme", place.Channel)
		if topic {
			path = view.ConversationURL("acme", place.Channel, &place.Topic)
		}
		step(t, p, "navigate to composer").MustNavigate(server.URL + path)
		step(t, p, "load composer").MustWaitLoad()
		step(t, p, "await composer textarea").MustElement("#message-body")
		if clearScript != nil {
			if err := (proto.PageRemoveScriptToEvaluateOnNewDocument{Identifier: clearScript.Identifier}).Call(step(t, p, "preserve storage on later navigation")); err != nil {
				t.Fatalf("remove draft storage isolation: %v", err)
			}
		}
		eval(t, p, "read draft scope", `() => { const f = document.querySelector('#message-composer').dataset, p = document.querySelector('#organization-stream').dataset;
   window.draftKey = 'ribbitto.draft.v1:' + p.member + ':' + p.session + ':' + f.draftChannel + ':' + (f.draftTopic || 'feed'); }`)
		if holdPosts.Load() {
			current := &postGate{done: make(chan struct{}), requests: make(chan pendingRequest, 8)}
			gate.Store(current)
			t.Cleanup(func() { close(current.done) })
		}
		return p
	}
	open := func(t *testing.T, topic bool, inject string) *rod.Page {
		t.Helper()
		return openPage(t, topic, inject, false)
	}
	openClean := func(t *testing.T, topic bool, inject string) *rod.Page {
		t.Helper()
		return openPage(t, topic, inject, true)
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

	restored := func(t *testing.T, p *rod.Page, want string) {
		t.Helper()
		step(t, p, "await restored textarea").MustWait(`want => document.querySelector('#message-body')?.value === want`, want)
		run(t, p, `check(restoreInputs === 0 && typingRequests === 0 && document.activeElement === restoreFocus, 'restore fired input, typing or focus');`)
	}
	reload := func(t *testing.T, p *rod.Page, want string) {
		t.Helper()
		step(t, p, "reload composer").MustReload()
		// Poll the current document instead of awaiting a load promise in a
		// context the reload can replace (as in navigateLoaded).
		waitState(t, p, "load reloaded composer", `() => document.readyState === 'complete'`, `() => JSON.stringify({readyState:document.readyState, href:location.href, composer:!!document.querySelector('#message-composer'), unfinished:performance.getEntriesByType('resource').filter(r => r.responseEnd === 0).slice(0,20).map(r => r.name)})`)
		restored(t, p, want)
	}
	for _, mode := range []string{"reload", "switch", "compositionend", "pagehide", "plain-422", "deadline"} {
		t.Run("restore/"+mode, func(t *testing.T) {
			p := openClean(t, false, "")
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
	for _, field := range []string{"valid", "member", "session", "expires", "old", "long", "future", "text-type", "at-type"} {
		t.Run("restore/guard/"+field, func(t *testing.T) {
			// Interactive precedes the deferred draft script. Each own-key
			// value differs in one field; sweep and restore share valid().
			p := open(t, false, fmt.Sprintf(`document.addEventListener('readystatechange', () => {
    if (document.readyState !== 'interactive') return;
    localStorage.clear(); const p=document.querySelector('#organization-stream').dataset;
    const e={member:p.member,session:p.session,expires:+p.sessionExpires,text:'seed',at:editNow,key:'00000000-0000-4000-8000-000000000001'};
    switch (%q) {
     case 'member': e.member='09000000-0000-0000-0000-000000000000'; break;
     case 'session': e.session='019a1b2c-3d50-7000-8000-000000000000'; break;
     case 'expires': e.expires++; break;
     case 'old': e.at-=7*86400000+1; break;
     case 'long': e.text='🐸'.repeat(4001); break;
     case 'future': e.at+=86400000+1; break;
     case 'text-type': e.text=['seed']; break;
     case 'at-type': e.at=String(e.at); break;
    }
    localStorage.setItem(scope(),JSON.stringify(e));
   });`, field))
			want := ""
			if field == "valid" {
				want = "seed"
			}
			restored(t, p, want)
		})
	}
	for _, want := range []string{"newer", ""} {
		t.Run("restore/future-edit/"+want, func(t *testing.T) {
			p := open(t, false, `document.addEventListener('readystatechange', () => {
    if (document.readyState !== 'interactive') return;
    localStorage.clear(); const p=document.querySelector('#organization-stream').dataset;
    localStorage.setItem(scope(),JSON.stringify({member:p.member,session:p.session,expires:+p.sessionExpires,text:'future',at:editNow+10000}));
   });`)
			restored(t, p, "future")
			eval(t, p, "edit future-dated restored draft", `want => { const at=entry().at; edit(want,editNow); releaseDrafts(); check(entry().text===want && entry().at>at,'restored clock rejected edit'); }`, want)
			closePage(t, p)
			copy := open(t, false, "")
			reload(t, copy, want)
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
	for _, mode := range []string{"remove", "reload", "reload-ordinary", "navigate-held", "restored-edit", "restored-edit-ordinary", "restored-empty", "other-topic", "other-channel", "other-member", "other-session", "expired-draft", "draft-key-type", "remove-different", "remove-inside", "remove-boundary", "remove-after", "remove-timer", "remove-reload", "remove-other-topic", "remove-other-channel", "stored-arrival"} {
		t.Run("keep-key/"+mode, func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := openClean(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `edit('original', editNow); document.querySelector('#message-composer').requestSubmit();`)
			first, release := nextPost(t)
			key := first.values.Get("idempotency_key")
			store := gate.Load()
			count := func() int {
				n := 0
				store.stored.Range(func(_, _ any) bool { n++; return true })
				return n
			}
			t.Run("stored-and-kept", func(t *testing.T) {
				if count() != 1 {
					t.Error("first request was not stored once")
				}
				eval(t, p, "draft saved before answer", `key => check(entry().text==='original' && entry().key===key, 'dispatch did not keep text/key')`, key)
			})
			if strings.HasSuffix(mode, "-ordinary") {
				step(t, p, "ordinary restored send").MustEvalOnNewDocument(`Object.defineProperty(crypto,'randomUUID',{value:undefined});`)
			}
			if mode != "navigate-held" {
				release(browserReply{status: 500})
				step(t, p, "unconfirmed send").MustWait(`() => !!document.querySelector('[data-state=failed][data-unconfirmed]')`)
			}
			wantReuse := mode == "remove" || mode == "reload" || mode == "reload-ordinary" || mode == "navigate-held" || mode == "remove-inside"
			navigate := func(path string) {
				step(t, p, "navigate draft place").MustNavigate(server.URL + path).MustWaitLoad()
			}
			switch {
			case strings.HasPrefix(mode, "remove"):
				// Freeze only the retention clock; exercise its exact boundary.
				run(t, p, `performance.now=()=>elapsed; document.querySelector('[data-pending-remove]').click(); check(!entry().key && entry().text==='', 'Remove retained storage');`)
				switch mode {
				case "remove-inside":
					run(t, p, `elapsed=299999;`)
				case "remove-boundary":
					run(t, p, `elapsed=300000;`)
				case "remove-after":
					run(t, p, `elapsed=300001;`)
				case "remove-timer":
					// Fire cleanup without advancing performance.now: a fresh key
					// then proves the map entry was deleted, not merely too old.
					run(t, p, `expireRemoved(); elapsed=0;`)
				case "remove-reload":
					reload(t, p, "")
				case "remove-other-topic", "remove-other-channel":
					// Simulate in-document navigation by changing the form's place
					// attributes: the same document and its hold map stay alive.
					// Draft saving still binds to its initial place, so read that
					// snapshot when checking confirmation after the simulated move.
					eval(t, p, "change place within document", `mode => { const f=document.querySelector('#message-composer'), source=scope();
      window.entry=()=>JSON.parse(localStorage.getItem(source));
      if (mode === 'remove-other-topic') f.dataset.draftTopic='03000000-0000-0000-0000-000000000000';
      else f.dataset.draftChannel='09000000-0000-0000-0000-000000000000'; }`, mode)
				}
			case mode == "navigate-held" || mode == "other-topic":
				navigate(view.ConversationURL("acme", place.Channel, &place.Topic))
				if mode == "navigate-held" {
					navigate(view.ChannelURL("acme", place.Channel))
					restored(t, p, "original")
					release(browserReply{status: 500})
				}
			case mode == "other-channel":
				navigate(view.ChannelURL("acme", kernel.ID{9}))
			case mode == "other-member" || mode == "other-session":
				previous := login.Load()
				next := *previous
				if mode == "other-member" {
					next.member = "09000000-0000-0000-0000-000000000000"
					otherMember.Store(true)
					defer otherMember.Store(false)
				} else {
					next.session = "019a1b2c-3d50-7000-8000-000000000000"
				}
				login.Store(&next)
				defer login.Store(previous)
				reload(t, p, "")
			case mode == "expired-draft":
				run(t, p, `const e=entry(); e.at=Date.now()-7*86400000; localStorage.setItem(scope(),JSON.stringify(e));`)
				reload(t, p, "")
			case mode == "draft-key-type":
				run(t, p, `const e=entry(); e.key=[e.key]; localStorage.setItem(scope(),JSON.stringify(e));`)
				reload(t, p, "original")
			default:
				reload(t, p, "original")
			}
			if mode == "navigate-held" || mode == "reload" || mode == "reload-ordinary" {
				t.Run("stored-and-kept-after-navigation", func(t *testing.T) {
					if count() != 1 {
						t.Error("navigation changed stored messages")
					}
					eval(t, p, "restored dispatch identity", `key => check(entry().key===key && document.querySelector('#message-body').value==='original', 'navigation lost kept text/key')`, key)
				})
			}
			if mode == "stored-arrival" {
				// A same-key resend gets the stored message through the actual
				// htmx response path, which makes the composer emit message-sent.
				run(t, p, `document.querySelector('#message-composer').requestSubmit();`)
				confirmed, confirm := nextPost(t)
				if confirmed.values.Get("idempotency_key") != key || count() != 1 {
					t.Error("stored-key lookup did not reuse the restored identity")
				}
				confirm(pendingAnswer(t, place.Channel))
				step(t, p, "stored arrival clears draft key").MustWait(`() => entry()?.key===null && entry()?.text==='' && sentEvents.length===1`)
				eval(t, p, "stored confirmation event", `key => check(sentEvents[0].key===key && !entry().key, 'stored arrival retained draft key')`, key)
			}
			if mode == "restored-empty" {
				run(t, p, `edit('',++editNow);`)
			}
			text := "original"
			if strings.HasPrefix(mode, "restored-edit") || mode == "remove-different" {
				text = "changed"
			}
			eval(t, p, "send kept text", `text => { if(document.querySelector('#message-body').value!==text) edit(text,++editNow); document.querySelector('#message-composer').requestSubmit(); }`, text)
			second, finish := nextPost(t)
			t.Run("actual-duplicate-on-resend", func(t *testing.T) {
				if second.values.Get("body") != text {
					t.Errorf("resent body=%q, want %q", second.values.Get("body"), text)
				}
				if got := second.values.Get("idempotency_key"); (got == key) != wantReuse {
					t.Errorf("key reused=%t, want %t", got == key, wantReuse)
				}
				want := 2
				if wantReuse {
					want = 1
				}
				if count() != want {
					t.Errorf("stored messages=%d, want %d", count(), want)
				}
			})
			finish(pendingAnswer(t, place.Channel))
			step(t, p, "confirmed resend clears saved key").MustWait(`() => entry()?.key===null && entry()?.text===''`)
		})
	}
	for _, pending := range []bool{false, true} {
		for _, mode := range []string{"success", "422", "404", "429", "500", "network", "reload-held", "typing-success", "typing-failure", "tab-write", "tab-write-dirty", "tab-write-newer-dirty", "composition", "409", "409-recovered", "409-changed", "busy"} {
			if pending && (mode == "busy" || strings.HasPrefix(mode, "tab-write-") || mode == "409-recovered" || mode == "409-changed") {
				continue
			}
			t.Run(fmt.Sprintf("results/pending=%t/%s", pending, mode), func(t *testing.T) {
				holdPosts.Store(true)
				defer holdPosts.Store(false)
				inject := ""
				if pending {
					inject = `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`
				}
				p := openClean(t, false, inject)
				run(t, p, `localStorage.clear(); edit('A', editNow); releaseDrafts(); edit('B', ++editNow); document.querySelector('#message-composer').requestSubmit(); check(entry().text === 'B' && pendingDrafts() === 0 && sendEvents.length === 1 && sendEvents[0].body === 'B', 'send snapshot/event');`)
				req, release := nextPost(t)
				eval(t, p, "check send identity and path", `(key, pending) => check(sendEvents[0].key === key && sendEvents[0].inPlace === !pending, 'send key/path')`, req.values.Get("idempotency_key"), pending)
				want, status := "", 200
				switch mode {
				case "422":
					status = 422
				case "404":
					status = 404
				case "409", "409-recovered", "409-changed":
					status = 409
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
				if mode == "busy" {
					run(t, p, `edit('', ++editNow); const before=localStorage.getItem(draftKey); document.querySelector('#message-composer').requestSubmit(); check(sendEvents.length===1 && localStorage.getItem(draftKey)===before && pendingDrafts()===1,'dropped submission snapshotted');`)
				}
				if mode == "tab-write-newer-dirty" {
					run(t, p, `edit('BC',editNow+100); tickDrafts(100); window.cAt=editNow;`)
				}
				if mode == "tab-write" || strings.HasPrefix(mode, "tab-write-") {
					other := open(t, false, "")
					otherAt := eval(t, p, "choose competing edit time", `newer => editNow+(newer ? 100 : 10000)`, mode == "tab-write-newer-dirty").Value.Num()
					eval(t, other, "save competing D", `at => { edit('D',at); releaseDrafts(); }`, otherAt)
					syncStorage(t, other, p)
					closePage(t, other)
					want = "D"
					if mode == "tab-write-dirty" {
						run(t, p, `edit('BC', editNow+20000); check(pendingDrafts()===1 && entry().text==='D','missing competing unsaved edit');`)
						want = "C"
					}
				}
				if mode == "tab-write-newer-dirty" {
					run(t, p, `editNow=entry().at+100; check(cAt<entry().at && entry().at<editNow && pendingDrafts()===1,'competing D must follow C before response');`)
				}
				if mode == "409-changed" {
					run(t, p, `edit('C',++editNow); releaseDrafts();`)
					want = "C"
				}
				if mode == "409-recovered" {
					want = ""
				}
				if mode == "reload-held" {
					reload(t, p, "B")
					release(browserReply{status: 500})
					return
				}
				reply := pendingAnswer(t, place.Channel)
				if status != 200 {
					reply = browserReply{status: status}
					if status == 409 {
						reply.body = pendingAnswer(t, place.Channel).body
						if !strings.Contains(string(reply.body), `data-posted-message="`+view.MessageDOMID(place.Channel)+`"`) {
							t.Fatal("409 fixture must name the stored message")
						}
						if !pending && mode != "409" {
							reply.body = []byte(strings.Replace(string(reply.body), `id="message-composer-state"`, `id="message-composer-state" data-sent-body="B"`, 1))
						}
						// Normally beforeSwap blocks pending failures. Exercise the
						// afterSwap status guard independently of that first guard.
						run(t, p, `window.failedSnapshot=localStorage.getItem(draftKey); document.addEventListener('htmx:beforeSwap', e => { if (e.detail.requestConfig?.elt.matches('[data-pending-form]') && e.detail.xhr.status===409) e.detail.shouldSwap=true; });`)
					}
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
					// Settle clears response routing before this delayed IME confirmation.
					step(t, p, "await send response settle during composition").MustWait(`() => swaps === 1`)
					run(t, p, `check(sentEvents.length === 0 || sendEvents[0].inPlace === false, 'sent before composition removal'); document.querySelector('#message-body').dispatchEvent(new Event('compositionend',{bubbles:true})); releaseDrafts();`)
				}
				if status == 409 && pending {
					step(t, p, "await forced pending 409 swap").MustWait(`() => swaps===1`)
					step(t, p, "await pending 409 failure with repeat").MustWait(`() => { const f=document.querySelector('[data-pending-form]'); return f?.dataset.state==='failed' && f.querySelector('[role=status]').textContent===document.querySelector('#message-send-alert').dataset.repeat; }`)
					step(t, p, "pending 409 must preserve snapshot without success").MustWait(`() => sentEvents.length===0 && localStorage.getItem(draftKey)===failedSnapshot`)
				}
				if status == 409 && !pending {
					step(t, p, "await in-place 409 swap").MustWait(`() => swaps===1`)
					if mode != "409-recovered" {
						run(t, p, `check(sentEvents.length===0 && localStorage.getItem(draftKey)===failedSnapshot,'unrecovered 409 confirmed or changed snapshot');`)
					}
				}
				if status == 200 || mode == "409-recovered" {
					waitState(t, p, "await confirmation event", `() => sentEvents.length === 1 && sentEvents[0].key === sendEvents[0].key && !!sentEvents[0].message`, `() => JSON.stringify({sendEvents, sentEvents, state:document.querySelector('#message-composer-state')?.dataset, compositionActive, pending:[...document.querySelectorAll('[data-pending-form]')].map(f => ({...f.dataset, key:f.elements.idempotency_key.value, item:f.closest('li').dataset}))})`)
				} else {
					run(t, p, `check(sentEvents.length === 0, 'failure emitted success');`)
				}
				if mode == "tab-write-dirty" || mode == "tab-write-newer-dirty" {
					run(t, p, `check(document.querySelector('#message-body').value==='C' && pendingDrafts()===1 && entry().text==='D','confirmation lost competing edit or forced save');`)
				}
				if mode == "tab-write-newer-dirty" {
					// The response must neither restamp C nor restart its remaining 200 ms.
					run(t, p, `tickDrafts(199); check(pendingDrafts()===1 && entry().text==='D','confirmation saved C early'); tickDrafts(1); check(pendingDrafts()===0 && entry().text==='D','confirmation restarted debounce or overwrote newer D');`)
				}
				// Pending composition intentionally holds its own edit until compositionend.
				run(t, p, `releaseDrafts();`)
				step(t, p, "await result draft value").MustWait(`want => entry()?.text === want`, want)
				reload(t, p, want)
			})
		}
	}
	for _, mode := range []string{"retry-success", "retry-failure", "retry-newer", "remove", "remove-newer"} {
		t.Run("pending-409/"+mode, func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := openClean(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `edit('A',editNow); document.querySelector('#message-composer').requestSubmit();`)
			first, release := nextPost(t)
			release(browserReply{status: 500})
			step(t, p, "await unconfirmed A before Edit").MustWait(`() => { const f=document.querySelector('[data-pending-form]'); return f?.dataset.state==='failed' && f.dataset.unconfirmed==='true' && !f.classList.contains('htmx-request'); }`)
			run(t, p, `document.querySelector('[data-pending-edit]').click(); edit('A2',++editNow); document.querySelector('#message-composer').requestSubmit();`)
			resend, release := nextPost(t)
			if resend.values.Get("idempotency_key") != first.values.Get("idempotency_key") {
				t.Fatal("Edit resend lost restored request key")
			}
			reply := pendingAnswer(t, place.Channel)
			reply.status = 409
			release(reply)
			step(t, p, "await 409 key rotation").MustWait(`() => { const f=document.querySelector('[data-pending-form]'); return f?.dataset.state==='failed' && !f.classList.contains('htmx-request') && f.elements.idempotency_key.value!==sendEvents[1].key; }`)
			run(t, p, `check(sendEvents.length===2 && sendEvents[1].key===sendEvents[0].key && sentEvents.length===0 && entry().text==='A2' && document.querySelector('#message-body').value==='','409 must keep restored-key snapshot'); window.rotatedKey=document.querySelector('[data-pending-form]').elements.idempotency_key.value;`)
			want, bodyWant := "", ""
			if strings.HasSuffix(mode, "newer") {
				want, bodyWant = "C", "C"
				run(t, p, `edit('C',++editNow); releaseDrafts(); window.newerSnapshot=localStorage.getItem(draftKey);`)
			}
			if strings.HasPrefix(mode, "retry-") {
				step(t, p, "Retry rotated item").MustEval(`() => document.querySelector('[data-pending-form]').requestSubmit()`)
				retry, release := nextPost(t)
				eval(t, p, "check rotated Retry identity", `key => check(key===rotatedKey && sendEvents.length===2,'Retry identity or message-sending changed')`, retry.values.Get("idempotency_key"))
				reply := pendingAnswer(t, place.Channel)
				if mode == "retry-failure" {
					want = "A2"
					reply = browserReply{status: 500}
				}
				release(reply)
				step(t, p, "await rotated Retry completion").MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
				eval(t, p, "check Retry confirmation correlation", `failed => check(failed ? sentEvents.length===0 : sentEvents.length===1 && sentEvents[0].key===sendEvents[1].key,'rotated Retry confirmation key or failure event')`, mode == "retry-failure")
			} else {
				run(t, p, `let action; document.addEventListener('message-pending-action',e=>action=e.detail,{once:true}); document.querySelector('[data-pending-remove]').click(); check(action?.key===sendEvents[1].key && action.kind==='remove','rotated Remove lost snapshot key');`)
			}
			eval(t, p, "check rotated action draft and textarea", `(want,bodyWant) => { releaseDrafts(); check(entry()?.text===want && document.querySelector('#message-body').value===bodyWant && sendEvents.length===2,'rotated action lost draft or textarea'); if (want==='C') check(localStorage.getItem(draftKey)===newerSnapshot,'rotated action replaced newer write'); }`, want, bodyWant)
			reload(t, p, want)
		})
	}
	for _, tc := range []struct {
		name, timing string
		status       int
	}{
		{"success", "saved", 200}, {"failure", "saved", 500}, {"reload-held", "saved", 0},
		{"save-pending", "between", 200}, {"save-pending-failure", "between", 500},
		{"response-before-save-success", "after", 200}, {"response-before-save-failure", "after", 500},
		{"unedited", "none", 200},
	} {
		t.Run("retry/"+tc.name, func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := openClean(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `localStorage.clear(); edit('A',editNow); document.querySelector('#message-composer').requestSubmit();`)
			first, release := nextPost(t)
			release(browserReply{status: 500})
			step(t, p, "await failed pending item").MustWait(`() => document.querySelector('[data-pending-form]')?.dataset.state === 'failed' && !document.querySelector('[data-pending-form].htmx-request')`)
			want := "C"
			if tc.timing == "none" {
				want = ""
				run(t, p, `check(entry().text==='A' && document.querySelector('#message-body').value==='' && pendingDrafts()===0,'unedited Retry state');`)
			} else {
				run(t, p, `edit('C',++editNow);`)
				if tc.timing == "saved" {
					run(t, p, `releaseDrafts();`)
				}
				eval(t, p, "assert newer text at Retry", `saved => check(document.querySelector('#message-body').value==='C' && (saved ? entry().text==='C' && pendingDrafts()===0 : entry().text==='A' && pendingDrafts()===1),'Retry dirty state missing')`, tc.timing == "saved")
			}
			step(t, p, "submit pending Retry").MustEval(`() => document.querySelector('[data-pending-form]').requestSubmit()`)
			retry, release := nextPost(t)
			if retry.values.Get("idempotency_key") != first.values.Get("idempotency_key") {
				t.Fatal("Retry changed send identity")
			}
			run(t, p, `check(sendEvents.length === 1, 'Retry emitted message-sending');`)
			if tc.timing == "between" {
				run(t, p, `check(pendingDrafts()===1,'Retry cancelled newer save'); releaseDrafts(); check(entry().text==='C','save between Retry and response lost C');`)
			}
			if tc.name == "reload-held" {
				reload(t, p, want)
				release(browserReply{status: 500})
				return
			}
			reply := pendingAnswer(t, place.Channel)
			if tc.status != 200 {
				reply = browserReply{status: tc.status}
			}
			release(reply)
			step(t, p, "await Retry outcome").MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
			if tc.timing == "after" {
				run(t, p, `check(pendingDrafts()===1 && entry().text==='A' && document.querySelector('#message-body').value==='C','Retry response changed unsaved C'); releaseDrafts();`)
			}
			step(t, p, "await Retry draft and textarea").MustWait(`want => entry()?.text === want && document.querySelector('#message-body').value === want`, want)
			reload(t, p, want)
		})
	}

	for _, mode := range []string{"edit", "edit-queued", "edit-backspace-pending", "edit-backspace-saved", "remove", "remove-newer", "remove-newer-send", "remove-tab-write"} {
		t.Run("pending-action/"+mode, func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := openClean(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
			run(t, p, `localStorage.clear(); edit('A',editNow); document.querySelector('#message-composer').requestSubmit();`)
			_, release := nextPost(t)
			if mode == "edit-queued" {
				run(t, p, `edit('B',++editNow); document.querySelector('#message-composer').requestSubmit();`)
			}
			release(browserReply{status: 422})
			step(t, p, "await pending action's failed item").MustWait(`() => { const f=document.querySelector('[data-pending-form]'); return f?.dataset.state==='failed' && !f.classList.contains('htmx-request'); }`)
			if mode == "edit-queued" {
				_, releaseB := nextPost(t)
				releaseB(pendingAnswer(t, place.Channel))
				step(t, p, "await queued B confirmation before Edit").MustWait(`() => sentEvents.length===1 && entry()?.text==='' && !document.querySelector('[data-pending-form].htmx-request')`)
			}
			if mode == "edit" {
				// A saved deletion, without input in this tab: only Edit may save A.
				run(t, p, `localStorage.setItem(draftKey,JSON.stringify({...entry(),text:'',at:++editNow})); check(entry().text==='' && pendingDrafts()===0,'missing saved tombstone before Edit');`)
			}
			if strings.HasPrefix(mode, "edit-backspace") {
				run(t, p, `edit('x',++editNow); edit('',++editNow); check(pendingDrafts()===1,'backspace save not pending');`)
				if mode == "edit-backspace-saved" {
					run(t, p, `releaseDrafts(); check(entry().text==='','missing backspace tombstone');`)
				}
			}
			if strings.HasPrefix(mode, "remove-newer") {
				run(t, p, `edit('C',++editNow); releaseDrafts(); check(entry().text==='C','newer Remove text not saved');`)
			}
			if mode == "remove-newer" {
				// Change the DOM without input: a bad replacement becomes observable
				// even though the pending-path edit's source remains null.
				run(t, p, `document.querySelector('#message-body').value='unsignalled';`)
			}
			if mode == "remove-newer-send" {
				// Sending C empties the textarea without input: an incorrect
				// replacement of A would now write a tombstone instead of C.
				run(t, p, `document.querySelector('#message-composer').requestSubmit();`)
				_, releaseC := nextPost(t)
				releaseC(browserReply{status: 422})
				step(t, p, "await newer C failure before Remove A").MustWait(`() => [...document.querySelectorAll('[data-pending-form]')].filter(f=>f.dataset.state==='failed').length===2 && !document.querySelector('[data-pending-form].htmx-request')`)
			}
			if mode == "remove-tab-write" {
				other := open(t, false, "")
				run(t, other, `edit('D',++editNow); releaseDrafts(); check(entry().text==='D','other tab did not save D');`)
				syncStorage(t, other, p)
				closePage(t, other)
				// Let a bad replacement pass the edit-time check as well;
				// only storage ownership should prevent overwriting D.
				run(t, p, `editNow=entry().at+1; check(pendingDrafts()===0 && document.querySelector('#message-body').value==='','Remove tab-write state');`)
			}
			edit := strings.HasPrefix(mode, "edit")
			want := ""
			if edit {
				want = "A"
			} else if strings.HasPrefix(mode, "remove-newer") {
				want = "C"
			} else if mode == "remove-tab-write" {
				want = "D"
			}
			bodyWant := want
			if mode == "remove-newer" {
				bodyWant = "unsignalled"
			}
			if mode == "remove-newer-send" || mode == "remove-tab-write" {
				bodyWant = ""
			}
			eval(t, p, "apply pending action without typing", `(edit,want,bodyWant) => {
    let action; document.addEventListener('message-pending-action',e=>action=e.detail,{once:true}); typingRequests=0;
    const f=document.querySelector('[data-pending-form][data-state="failed"]'), key=f.elements.idempotency_key.value, inputs=restoreInputs, typing=typingRequests;
    const button=f.querySelector(edit?'[data-pending-edit]':'[data-pending-remove]'); check(!button.disabled,'pending action disabled'); button.click();
    check(action?.key===key && action.kind===(edit?'edit':'remove'),'pending action key or bubbling');
    check(restoreInputs===inputs && typingRequests===typing,'pending action emitted input or typing');
    if (edit) check(entry().text===want && pendingDrafts()===0 && typingRequests===0,'Edit did not save restored text immediately without typing');
    else check(entry().text===want,'Remove replaced a newer stored draft');
    releaseDrafts(); check(entry().text===want && document.querySelector('#message-body').value===bodyWant,'pending action lost draft or stale timer won');
   }`, edit, want, bodyWant)
			reload(t, p, want)
		})
	}

	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue/success=%t", success), func(t *testing.T) {
			holdPosts.Store(true)
			defer holdPosts.Store(false)
			p := openClean(t, false, `Object.defineProperty(crypto,'randomUUID',{value:draftUUID});`)
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
			// A fresh document restores B while its original POST remains held.
			restored(t, copy, "B")
			closePage(t, copy)
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
		p := openClean(t, false, "")
		run(t, p, `localStorage.clear(); hide(); flush(); check(!entry(), 'clean page wrote');
   const at = editNow; edit('first', at - 1); tickDrafts(299); check(!entry(), 'early save');
   edit('  🐸\n', at); tickDrafts(299); check(!entry(), 'debounce did not restart'); editNow += 50; tickDrafts(1);
   const e = entry(), page = document.querySelector('#organization-stream').dataset;
   check(e.text === '  🐸\n' && e.at === at && e.member === page.member && e.session === page.session && e.expires === +page.sessionExpires && e.key === null, 'value or input time');
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
			// Clear before restore seeds either page's edit clock. A is still
			// unsaved when B opens, so B cannot erase a needed write.
			a, b := openClean(t, false, ""), openClean(t, false, "")
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
			a, b := openClean(t, false, ""), openClean(t, true, "")
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
		p := openClean(t, false, "")
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
		p := openClean(t, false, "")
		run(t, p, `localStorage.clear(); edit('own', editNow); flush(); const e = entry();
   for (let n=1; n<50; n++) localStorage.setItem(draftKey.replace(/feed$/, '00000000-0000-0000-0000-' + n.toString(16).padStart(12,'0')), JSON.stringify({...e, at:editNow - n}));
   edit('updated', editNow + 1); releaseDrafts(); check(entry().text === 'updated' && Object.keys(localStorage).length === 50, 'update evicted another place');`)
	})
	// Each foreign set differs in just one owner field: neither predicate
	// can accidentally be covered by the other one excluding the same keys.
	for _, field := range []string{"member", "session"} {
		t.Run("scope/"+field, func(t *testing.T) {
			p := openClean(t, false, "")
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
		a, b := openClean(t, false, ""), openClean(t, false, "")
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
		run(t, other, `releaseDrafts(); hide(); check(!Object.keys(localStorage).some(k => k.includes('`+session+`')), 'sign-out pending save recreated old-session key');`)
		previous := login.Load()
		later := *previous
		later.session = "019a1b2c-3d50-7000-8000-000000000000"
		login.Store(&later)
		defer login.Store(previous)
		closePage(t, p)
		closePage(t, other)
		fresh := open(t, false, "")
		restored(t, fresh, "")
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
			closePage(t, a)
			step(t, b, "reload later session").MustNavigate(step(t, b, "read later-session URL").MustInfo().URL)
			step(t, b, "load later session").MustWaitLoad()
			run(t, b, `check(!Object.keys(localStorage).some(k => k.includes('`+session+`')) && document.querySelector('#message-body').value === '', 'later load restored stale entry');`)
		})
	}
	// Fresh foreign sessions survive the load sweep, so only expiry can disturb
	// these controls. A second own place catches cleanup limited to the open key.
	const seedExpiry = `const page=document.querySelector('#organization-stream').dataset;
   window.expires=+page.sessionExpires; editNow=expires-1000; localStorage.clear();
   const otherSession='019a1b2c-3d51-7000-8000-000000000000', otherMember='09000000-0000-0000-0000-000000000000';
   const e={member:page.member,session:page.session,expires,text:'seed',at:editNow,key:null};
   window.ownKeys=[scope(),scope().replace(/feed$/, '03000000-0000-0000-0000-000000000000')];
   window.foreignKeys=[scope().replace(page.session,otherSession),scope().replace(page.session,otherSession).replace(page.member,otherMember)];
   ownKeys.forEach(k=>localStorage.setItem(k,JSON.stringify(e)));
   foreignKeys.forEach((k,i)=>localStorage.setItem(k,JSON.stringify({...e,session:otherSession,member:i ? otherMember : page.member})));
   window.foreignBefore=foreignKeys.map(k=>localStorage.getItem(k));
   window.marker=document.createElement('span'); marker.dataset.draftMarker=''; marker.hidden=true;
   const anchor=document.createElement('a'); anchor.href=location.pathname+'/topics/03000000-0000-0000-0000-000000000000';
   anchor.append(marker); document.querySelector('#sidebar-channels').append(anchor);
   window.refreshExpiry=()=>dispatchEvent(new StorageEvent('storage',{key:scope()})); refreshExpiry();
   window.draftWrites=0; window.draftRemovals=[]; window.expiryErrors=0; addEventListener('error',()=>expiryErrors++);
   const set=Storage.prototype.setItem, remove=Storage.prototype.removeItem;
   // Other scripts may touch unrelated keys while the page loads.
   Storage.prototype.setItem=function(k,v){if(String(k).startsWith('ribbitto.draft.v1:')) draftWrites++; return set.call(this,k,v);};
   Storage.prototype.removeItem=function(k){if(String(k).startsWith('ribbitto.draft.v1:')) draftRemovals.push(k); return remove.call(this,k);};`
	const checkExpiry = `check(ownKeys.every(k=>localStorage.getItem(k)===null),'own places survived expiry');
   check(foreignKeys.every((k,i)=>localStorage.getItem(k)===foreignBefore[i]),'foreign session/member changed');
   check(draftWrites===0,'expiry wrote '+draftWrites); check(draftRemovals.length===2,'expiry removals '+JSON.stringify(draftRemovals)); check(pendingDrafts()===0,'expiry left debounce');
   check(document.querySelector('#message-body').value===want && marker.hidden,'expiry changed textarea or left marker');
   // Neither clock rolling back nor another timer/event may restart or repeat cleanup.
   editNow=expires-1000; elapsed=0; edit(want,editNow); releaseDrafts(); flush(); if(typeof expireDrafts==='function') expireDrafts(); refreshExpiry();
   const sends=sendEvents.length, errors=expiryErrors, b=document.querySelector('#message-body'); // Other scripts may already have failed on the removed sidebar.
   b.dispatchEvent(new CustomEvent('message-sending',{bubbles:true,detail:{key:'00000000-0000-4000-8000-000000000001',body:b.value,inPlace:true}}));
   check(sendEvents.length===sends+1,'sending dispatch lost '+sendEvents.length+'/'+sends); check(expiryErrors===errors,'sending dispatch raised errors'); check(b.value===want,'sending changed text');
   check(draftWrites===0 && draftRemovals.length===2 && ownKeys.every(k=>localStorage.getItem(k)===null),'stopped page resumed');
   check(foreignKeys.every((k,i)=>localStorage.getItem(k)===foreignBefore[i]),'later cleanup changed foreign drafts');`
	t.Run("expiry-clock/normal", func(t *testing.T) {
		p := openClean(t, false, "")
		run(t, p, seedExpiry)
		run(t, p, `check(!marker.hidden,'normal marker missing'); edit('edited',editNow+1);
   tickDrafts(299); check(entry().text==='seed' && draftWrites===0 && pendingDrafts()===1,'early normal save');
   tickDrafts(1); check(entry().text==='edited' && draftWrites===1 && pendingDrafts()===0,'normal debounce did not save');
   expireDrafts(); refreshExpiry(); check(!marker.hidden && draftRemovals.length===0,'normal expiry removed keys or hid marker');
   check(ownKeys.every(k=>localStorage.getItem(k)!==null) && foreignKeys.every((k,i)=>localStorage.getItem(k)===foreignBefore[i]),'normal keys changed');`)
		before := eval(t, p, "read normal drafts", `()=>snapshot()`).Value.Str()
		copy := open(t, false, `document.addEventListener('readystatechange',()=>{if(document.readyState==='interactive') editNow=+document.querySelector('#organization-stream').dataset.sessionExpires-999;});`)
		restored(t, copy, "edited")
		eval(t, copy, "restore preserves normal drafts", `before=>check(snapshot()===before,'restore removed or changed keys')`, before)
	})
	for _, path := range []string{"save", "edit", "restore", "timer", "markers", "monotonic", "boundary"} {
		t.Run("expiry-clock/"+path, func(t *testing.T) {
			var p *rod.Page
			want := "kept"
			if path == "restore" {
				// Isolate restore from the initial marker check; the timer runs
				// later, so a missed restore guard would visibly restore seed text.
				p = open(t, false, `document.addEventListener('readystatechange',()=>{
    if(document.readyState!=='interactive') return;
    `+seedExpiry+` document.querySelector('#sidebar-channels').remove(); editNow=expires+1; });`)
				want = ""
			} else {
				p = openClean(t, false, "")
				run(t, p, seedExpiry)
				run(t, p, `check(!marker.hidden,'normal marker missing'); edit('kept',editNow+1); check(pendingDrafts()===1,'normal edit did not queue');`)
				eval(t, p, "drive isolated expiry path", `path=>{
    check(elapsed===0,'monotonic clock already advanced');
    if(path==='boundary') {
     edit('kept',expires-1); releaseDrafts(); check(entry().text==='kept' && draftWrites===1 && draftRemovals.length===0,'expires-1 stopped saving'); draftWrites=0;
    }
    editNow=path==='boundary' ? expires : expires+1;
    if(path==='monotonic') {editNow=expires-1; elapsed=31*86400000;}
    switch(path) {
     case 'save': tickDrafts(299); check(draftWrites===0 && pendingDrafts()===1,'debounce not exercised'); tickDrafts(1); break;
     case 'edit': case 'boundary': edit('kept',editNow); break;
     case 'timer': case 'monotonic': expireDrafts(); break;
     case 'markers': refreshExpiry(); break;
    }
   }`, path)
			}
			run(t, p, `const want=`+fmt.Sprintf("%q", want)+`; `+checkExpiry)
		})
	}
	t.Run("expiry", func(t *testing.T) {
		p := openClean(t, false, "")
		run(t, p, `localStorage.clear(); edit('saved', editNow); flush(); edit('pending', editNow + 1); elapsed = 31*86400000; expireDrafts();
   check(!entry() && pendingDrafts() === 0, 'expiry cleanup/cancellation'); const before = snapshot();
   releaseDrafts(); edit('expired', editNow + 1); flush(); hide(); check(snapshot() === before, 'expiry wrote');
   elapsed = 0; edit('clock reset after deadline', editNow + 1); releaseDrafts(); check(snapshot() === before, 'expiry did not permanently stop');`)
	})
	for _, trigger := range []string{"input", "save"} {
		t.Run("deadline guard/"+trigger, func(t *testing.T) {
			p := openClean(t, false, "")
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
