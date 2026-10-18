package web

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
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
	setup := func(s *Services) { s.Sessions, s.SignIn = login, signIn }
	handler, _, _, place := composerHandler(t, true, setup)
	feedHandler, _, _, _ := composerHandler(t, false, setup)
	var noticeOnly atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signin" {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
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
	defer server.Close()
	open := func(t *testing.T, topic bool, inject string) *rod.Page {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		t.Cleanup(cancel)
		p := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		t.Cleanup(func() { p.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
		// Only the drafts debounce is gated, including cancellation. Lifecycle
		// events and clocks are controlled independently of focus and real sleeps.
		p.MustEvalOnNewDocument(`(() => {
   const timers = new Map(), set = window.setTimeout, clear = window.clearTimeout; let id = 0;
   window.setTimeout = (f, d, ...a) => { if (d > 60000) window.expireDrafts = () => f(...a); return d === 300 ? (timers.set(--id, {run: () => f(...a), left:d}), id) : set(f, d, ...a); };
   window.clearTimeout = id => { timers.delete(id); clear(id); };
   window.tickDrafts = ms => { for (const [id, task] of [...timers]) { task.left -= ms; if (task.left <= 0) { timers.delete(id); task.run(); } } };
   window.releaseDrafts = () => tickDrafts(300);
   window.pendingDrafts = () => timers.size;
   window.editNow = Date.now(); Date.now = () => editNow;
   const now = performance.now.bind(performance); window.elapsed = 0; performance.now = () => now() + elapsed;
   window.draftHidden = false; Object.defineProperty(document, 'hidden', {get: () => draftHidden});
   Object.defineProperty(crypto, 'randomUUID', {value: undefined});
   window.swaps = 0; window.signals = 0;
   document.addEventListener('htmx:afterSwap', e => { if (e.target.id === 'message-composer-state') window.swapStatus = e.detail.xhr.status; });
   document.addEventListener('htmx:afterSettle', e => { if (e.target.id === 'message-composer-state') swaps++; });
   document.addEventListener('htmx:afterRequest', e => { if (e.detail.elt.id === 'typing-form' && e.detail.xhr.status === 204) signals++; });
   window.check = (ok, why) => { if (!ok) throw new Error(why); };
   window.edit = (text, at) => { editNow = at; const b = document.querySelector('#message-body'); b.value = text; b.dispatchEvent(new Event('input', {bubbles:true})); };
   window.flush = () => { draftHidden = true; document.dispatchEvent(new Event('visibilitychange')); draftHidden = false; };
   window.hide = () => dispatchEvent(new Event('pagehide'));
   window.entry = () => JSON.parse(localStorage.getItem(draftKey));
   window.snapshot = () => JSON.stringify(Object.entries(localStorage).sort());
  })();
` + inject)
		path := view.ChannelURL("acme", place.Channel)
		if topic {
			path = view.ConversationURL("acme", place.Channel, &place.Topic)
		}
		p.MustNavigate(server.URL + path).MustWaitLoad()
		p.MustEval(`() => { const f = document.querySelector('#message-composer').dataset, p = document.querySelector('#organization-stream').dataset;
   window.draftKey = 'ribbitto.draft.v1:' + p.member + ':' + p.session + ':' + f.draftChannel + ':' + (f.draftTopic || 'feed'); }`)
		return p
	}
	run := func(t *testing.T, p *rod.Page, code string) { t.Helper(); p.MustEval(`() => { ` + code + ` }`) }
	// Sequential CDP evaluations do not order storage propagation to another
	// tab. A unique marker also orders a remove/restore whose final snapshot
	// equals the reader's old one; waiting for that snapshot alone could pass early.
	storageSync := 0
	syncStorage := func(t *testing.T, from, to *rod.Page) {
		t.Helper()
		const marker = "ribbitto.test-storage-barrier"
		storageSync++
		want := from.MustEval(`(key, value) => { localStorage.setItem(key, value); return snapshot(); }`, marker, storageSync).Str()
		to.MustWait(`want => snapshot() === want`, want)
		from.MustEval(`key => localStorage.removeItem(key)`, marker)
		to.MustWait(`key => localStorage.getItem(key) === null`, marker)
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
			b.MustEval(`(mode, aAt) => { edit('B', mode === 'equal' ? aAt : editNow + 1000); flush();
    check(entry().text === 'B' && (mode !== 'equal' || entry().at === aAt), 'B save or equal input time');
    if (mode === 'deletion' || mode === 'over-limit') { edit(mode === 'deletion' ? ' ' : '🐸'.repeat(4001), editNow + 1); flush(); } }`, mode, a.MustEval(`() => editNow`).Num())
			syncStorage(t, b, a)
			run(t, a, `const winner = localStorage.getItem(draftKey); releaseDrafts(); check(localStorage.getItem(draftKey) === winner, 'older/equal save won');
    localStorage.removeItem(draftKey); hide(); flush(); check(!entry(), 'skipped save stayed dirty'); localStorage.setItem(draftKey, winner);`)
			syncStorage(t, a, b)
			if mode == "overlap" {
				// Both reads could have preceded either write: apply A's stale write.
				b.MustEval(`at => { const older = {...entry(), text:'A', at}; localStorage.setItem(draftKey, JSON.stringify(older));
     hide(); flush(); check(entry().text === 'A' && document.querySelector('#message-body').value === 'B', 'clean tab repaired overlap');
     edit('B again', editNow + 1); releaseDrafts(); check(entry().text === 'B again', 'next input did not recover'); }`, a.MustEval(`() => editNow`).Num())
			} else {
				b.MustEval(`mode => check(entry().text === (mode === 'deletion' || mode === 'over-limit' ? '' : 'B'), 'stored winner')`, mode)
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
			p.MustEval(`field => { localStorage.clear(); edit('seed', editNow); flush(); const e = entry(); localStorage.clear();
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
		k := b.MustEval(`() => oldest`).Str()
		run(t, b, `edit('B newer', editNow + 1000); flush();`)
		syncStorage(t, b, a)
		a.MustEval(`k => { localStorage.removeItem(k); edit('A', editNow + 2000); flush(); check(!localStorage.getItem(k) && Object.keys(localStorage).length === 50, 'eviction race'); }`, k)
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
		wait := a.WaitNavigation(proto.PageLifecycleEventNameLoad)
		a.MustElement(`form[action="/signout"]`).MustEval(`function() { this.requestSubmit(); }`)
		wait()
		a.MustEval(`h => localStorage.setItem('ribbitto.signed-out.v1', h)`, "019a1b2c-3d4f-7000-8000-000000000000")
		b.MustWait(`() => notified`)
		run(t, b, `check(entry()?.text === 'saved', 'cleanup ran before stop assertion');
   check(pendingDrafts() === 0, 'notice did not cancel save'); const before = snapshot();
   releaseDrafts(); edit('after sign-out', editNow + 1); flush(); hide(); check(snapshot() === before && pendingDrafts() === 0, 'ended session wrote');`)
	})
	t.Run("sign-out cleanup", func(t *testing.T) {
		p := open(t, false, "")
		run(t, p, `localStorage.clear(); edit('ended session', editNow); flush();`)
		wait := p.WaitNavigation(proto.PageLifecycleEventNameLoad)
		p.MustElement(`form[action="/signout"]`).MustEval(`function() { this.requestSubmit(); }`)
		wait()
		if !strings.HasSuffix(p.MustInfo().URL, "/signin") {
			t.Fatal("sign-out did not reach sign-in")
		}
		run(t, p, `check(!Object.keys(localStorage).some(k => k.startsWith('ribbitto.draft.v1:')), 'sign-out cleanup');`)
	})
	t.Run("later session interleaving", func(t *testing.T) {
		a := open(t, false, "")
		run(t, a, `localStorage.clear(); edit('pending old session', editNow + 100);`)
		next := *login.Load()
		next.session = "019a1b2c-3d50-7000-8000-000000000000"
		login.Store(&next)
		t.Cleanup(func() {
			login.Store(&draftsLogin{"08000000-0000-0000-0000-000000000000", session, time.Now().Add(30 * 24 * time.Hour)})
		})
		b := open(t, false, "")
		run(t, a, `releaseDrafts(); check(entry().text === 'pending old session', 'stale write missing');`)
		syncStorage(t, a, b)
		b.MustNavigate(b.MustInfo().URL).MustWaitLoad()
		run(t, b, `check(!Object.keys(localStorage).some(k => k.includes('`+session+`')), 'later load kept stale entry');`)
	})
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
			p.MustEval(`trigger => { localStorage.clear(); if (trigger === 'save') edit('pending', editNow);
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
			p.MustWait(`() => signals > 0`)
			for _, text := range []string{" ", "sent"} {
				status, topic := 200, "03000000-0000-0000-0000-000000000000"
				if text == " " {
					status = 422
				}
				if mode == "feed" {
					topic = ""
				}
				p.MustEval(`text => { document.querySelector('#message-body').value = text; document.querySelector('#message-composer').requestSubmit(); }`, text)
				p.MustWait(`() => swaps > 0 && !document.querySelector('#message-composer button').disabled`)
				p.MustEval(`(status, topic) => { const f = document.querySelector('#message-composer'); check(swapStatus === status && f.dataset.draftChannel === '01000000-0000-0000-0000-000000000000' && (f.dataset.draftTopic || '') === topic, 'swap status or place'); swaps = 0; }`, status, topic)
			}
			run(t, p, `check(document.querySelector('#message-body').value === '', 'sending broken');`)
		})
	}
}
