package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testDraftMarkersBrowser(t *testing.T, browser *rod.Browser) {
	session, _ := pathID("019a1b2c-3d4e-7800-8800-000000000000")
	snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}},
		Channels: []view.Channel{{ID: kernel.ID{1}, Name: "general"}, {ID: kernel.ID{4}, Name: "other"}},
		Topics:   []view.Topic{{ID: kernel.ID{2}, Name: "draft topic"}, {ID: kernel.ID{3}, Name: "open topic"}},
		Topic:    &view.Topic{ID: kernel.ID{3}}, MemberID: kernel.ID{8}, Session: session,
		SessionExpires: time.Now().Add(24 * time.Hour), Rendered: time.Now(), EventCursor: new(int64(42)),
		Composer: new("generation"), IdempotencyKey: new("00000000-0000-4000-8000-000000000001")}
	var streams draftMarkerStreams
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page := snapshot
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			stream, release := streams.subscribe(r.Context())
			defer release()
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			for err := sender.Heartbeat(r.Context()); err == nil; {
				select {
				case out := <-stream.frames:
					err = sender.Send(r.Context(), out)
				case <-r.Context().Done():
					return
				}
			}
		case strings.HasSuffix(r.URL.Path, "/resync"):
			// Match resyncSidebar: the POST acknowledges the request; the
			// replacement arrives on the stream, with its synchronous settle.
			streams.publish(realtime.Outgoing{Ephemeral: true, Name: "sidebar", Data: []byte(renderIn(t, "en", view.SidebarLists(page)))})
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST":
			page.PostedMessageID = new(kernel.ID{9})
			page.IdempotencyKey = new("00000000-0000-4000-8000-000000000002")
			templ.Handler(view.MessageComposer(page)).ServeHTTP(w, r)
		default:
			if strings.Contains(r.URL.Path, "/topics/020") {
				page.Topic = &page.Topics[0]
			}
			if strings.HasSuffix(r.URL.Path, "/channels") {
				templ.Handler(view.ChannelListScreen("/static/css/app.css", page)).ServeHTTP(w, r)
			} else {
				templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
			}
		}
	})
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(catalogues.Middleware(mux))
	defer server.Close()
	step := func(t *testing.T, p *rod.Page, name string) *rod.Page {
		t.Helper()
		bounded := p.Timeout(3 * time.Second).WithPanic(func(v any) { t.Fatalf("%s: %v", name, v) })
		t.Cleanup(func() { bounded.CancelTimeout() })
		return bounded
	}
	wait := func(t *testing.T, p *rod.Page, name, condition string) {
		t.Helper()
		if err := step(t, p, name).Wait(rod.Eval(condition)); err != nil {
			diagnostic := step(t, p.Context(context.Background()), "read marker failure state").MustEval(`() => JSON.stringify({url:location.href, opens, storage:Object.entries(localStorage), markers:[...document.querySelectorAll('[data-draft-marker]')].map(m=>[m.closest('a').href,m.hidden])})`).Str()
			t.Fatalf("%s: %v; observed %s", name, err, diagnostic)
		}
	}
	open := func(t *testing.T, path string, clean bool) *rod.Page {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		t.Cleanup(cancel)
		creating, stopCreating := context.WithCancel(ctx)
		t.Cleanup(stopCreating)
		timer := time.AfterFunc(3*time.Second, stopCreating)
		p, err := browser.Context(creating).Page(proto.TargetCreateTarget{})
		timer.Stop()
		if err != nil {
			t.Fatalf("create marker page: %v", err)
		}
		p = p.Context(ctx)
		t.Cleanup(func() { step(t, p.Context(context.Background()), "close marker page").MustClose() })
		step(t, p, "install marker controls").MustEvalOnNewDocument(`(() => {
   window.pageErrors=0; window.addEventListener('error',()=>pageErrors++);
   window.opens=0; document.addEventListener('htmx:sseOpen',()=>opens++);
   const now=performance.now.bind(performance), set=window.setTimeout; window.elapsed=0;
   performance.now=()=>now()+elapsed;
   window.setTimeout=(f,d,...a)=>{ if(d>60000) window.expireMarkers=()=>f(...a); return set(f,d,...a); };
   window.marked=id=>{const m=document.querySelector('#link-'+id+' [data-draft-marker]'); return m ? !m.hidden : null;};
   window.put=(place='feed', change={}, member, session, channel='01000000-0000-0000-0000-000000000000')=>{
    const p=document.querySelector('#organization-stream').dataset;
    const key='ribbitto.draft.v1:'+(member||p.member)+':'+(session||p.session)+':'+channel+':'+place;
    localStorage.setItem(key,JSON.stringify({member:p.member,session:p.session,expires:Number(p.sessionExpires),text:'unfinished',at:Date.now(),...change}));
    return key;
   };
   window.refresh=()=>dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));
  })()`)
		step(t, p, "navigate marker setup").MustNavigate(server.URL + "/healthz")
		step(t, p, "load marker setup").MustWaitLoad()
		if clean {
			step(t, p, "clear marker storage").MustEval(`() => localStorage.clear()`)
		}
		step(t, p, "navigate marker page").MustNavigate(server.URL + path)
		step(t, p, "load marker page").MustWaitLoad()
		wait(t, p, "connect marker stream", `() => opens>0 && !!document.querySelector('[data-draft-marker]')`)
		return p
	}
	const channel = "channel-01000000000000000000000000000000"
	const topic = "topic-02000000000000000000000000000000"
	const sibling = "topic-03000000000000000000000000000000"
	path := view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{3})
	for _, tc := range []struct{ name, seed, want string }{
		{"non-empty-feed", `put()`, `marked('` + channel + `') && !marked('` + topic + `')`},
		{"topic-and-channel-only", `put('02000000-0000-0000-0000-000000000000')`, `marked('` + channel + `') && marked('` + topic + `') && !marked('` + sibling + `')`},
		{"own-member-only", `put('feed',{},'09000000-0000-0000-0000-000000000000')`, `marked('` + channel + `') === false`},
		{"own-session-only", `put('feed',{},null,'019a1b2c-3d4d-7800-8800-000000000000')`, `marked('` + channel + `') === false`},
		{"value-member", `put('feed',{member:'09000000-0000-0000-0000-000000000000'})`, `marked('` + channel + `') === false`},
		{"value-session", `put('feed',{session:'019a1b2c-3d4d-7800-8800-000000000000'})`, `marked('` + channel + `') === false`},
		{"value-expiry", `put('feed',{expires:0})`, `marked('` + channel + `') === false`},
		{"unlisted-topic", `put('02000000-0000-0000-0000-000000000000',{},null,null,'04000000-0000-0000-0000-000000000000')`, `marked('channel-04000000000000000000000000000000') === true && marked('` + topic + `') === false`},
		{"over-limit", `put('feed',{text:'x'.repeat(4001)})`, `marked('` + channel + `') === false`},
		{"invalid-value", `put('feed',{at:0})`, `marked('` + channel + `') === false`},
		{"malformed-key", `localStorage.setItem(put()+':invalid',localStorage.getItem(put())); localStorage.removeItem(put())`, `marked('` + channel + `') === false`},
		{"tombstone", `put('feed',{text:''})`, `marked('` + channel + `') === false`},
		{"retention", `put(); put('02000000-0000-0000-0000-000000000000',{at:Date.now()-7*86400000})`, `marked('` + channel + `') && !marked('` + topic + `')`},
		{"open-conversation", `put('03000000-0000-0000-0000-000000000000')`, `marked('` + channel + `') && !marked('` + sibling + `')`},
		{"page-deadline", `put(); elapsed=2*86400000`, `marked('` + channel + `') === false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := open(t, path, true)
			step(t, p, "seed isolated marker predicate").MustEval(`() => { ` + tc.seed + `; refresh(); }`)
			wait(t, p, tc.name, `() => `+tc.want)
			reading := step(t, p, "read consistent accessible channel name")
			for {
				link := reading.MustElement("#link-" + channel)
				state := `function () { return {connected:this.isConnected, shown:!this.querySelector('[data-draft-marker]').hidden}; }`
				before := link.MustEval(state)
				if !before.Get("connected").Bool() {
					continue
				}
				tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: link.Object.ObjectID}).Call(reading)
				if err != nil {
					t.Fatalf("channel accessible name: %v", err)
				}
				after := link.MustEval(state)
				// A resync can replace the row between protocols. Re-read only
				// when this very row detached or its visibility changed; a stable
				// AX/DOM disagreement must fail, never wait for agreement.
				if !after.Get("connected").Bool() || before.Get("shown").Bool() != after.Get("shown").Bool() {
					continue
				}
				if len(tree.Nodes) == 0 || tree.Nodes[0].Name == nil {
					t.Fatalf("channel accessible name: tree=%+v", tree)
				}
				shown := after.Get("shown").Bool()
				if got := tree.Nodes[0].Name.Value.Str(); strings.Contains(got, "Draft") != shown {
					t.Fatalf("accessible name %q, marker shown=%t", got, shown)
				}
				break
			}
		})
	}
	t.Run("cleanup-retention", func(t *testing.T) {
		p := open(t, path, true)
		clock := step(t, p, "seed retention boundary").MustEval(`() => {
   const now=Date.now();
   put('feed',{at:now-7*86400000+1});
   put('02000000-0000-0000-0000-000000000000',{at:now-7*86400000});
   return now;
  }`).Int()
		step(t, p, "freeze cleanup clock").MustEvalOnNewDocument(`Date.now=()=>` + strconv.Itoa(clock))
		step(t, p, "reload at retention boundary").MustReload().MustWaitLoad()
		if !step(t, p, "read retention cleanup").MustEval(`() => {
   const d=document.querySelector('#organization-stream').dataset;
   const prefix='ribbitto.draft.v1:'+d.member+':'+d.session+':01000000-0000-0000-0000-000000000000:';
   return localStorage.getItem(prefix+'feed')!==null && localStorage.getItem(prefix+'02000000-0000-0000-0000-000000000000')===null;
  }`).Bool() {
			t.Fatal("cleanup must keep drafts younger than seven days and remove drafts at seven days")
		}
	})
	t.Run("sending-snapshot", func(t *testing.T) {
		p := open(t, path, true)
		if !step(t, p, "save sending snapshot at edit time").MustEval(`() => {
   const now=Date.now(), key=put('03000000-0000-0000-0000-000000000000',{at:now-1000});
   Date.now=()=>now;
   const body=document.querySelector('#message-body'); body.value='typed';
   body.dispatchEvent(new CustomEvent('message-sending',{bubbles:true,detail:{key:'00000000-0000-4000-8000-000000000001',body:'sent',inPlace:false}}));
   const saved=JSON.parse(localStorage.getItem(key));
   return saved.at===now && saved.text==='sent' && saved.key==='00000000-0000-4000-8000-000000000001';
  }`).Bool() {
			t.Fatal("sending must save the sent body and key at the edit time")
		}
	})
	t.Run("sending-with-stored-json-string", func(t *testing.T) {
		p := open(t, path, true)
		if !step(t, p, "trim and send with malformed stored values").MustEval(`() => {
   const now=Date.now(), keys=[];
   Date.now=()=>now;
   for(let i=0;i<50;i++) keys.push(put('feed',{at:now-1000+i},null,null,'02000000-0000-4000-8000-'+String(i+10).padStart(12,'0')));
   const key=put('03000000-0000-0000-0000-000000000000',{at:now});
   localStorage.setItem(keys[1],'"abc"'); localStorage.setItem(key,'"abc"');
   const removed=[], remove=Storage.prototype.removeItem;
   Storage.prototype.removeItem=function(k){removed.push(k); return remove.call(this,k);};
   document.querySelector('#message-body').dispatchEvent(new CustomEvent('message-sending',{bubbles:true,detail:{key:'00000000-0000-4000-8000-000000000001',body:'sent',inPlace:true}}));
   return removed.length===1 && removed[0]===keys[0] && localStorage.getItem(keys[0])===null && localStorage.getItem(keys[1])==='"abc"' && JSON.parse(localStorage.getItem(key)).at===null;
  }`).Bool() {
			t.Fatal("sending with stored JSON strings must preserve the trim and timestamp results")
		}
	})
	t.Run("sending-detects-expiry", func(t *testing.T) {
		p := open(t, path, true)
		step(t, p, "send after wall-clock expiry").MustEval(`() => {
   const keys=[put(),put('02000000-0000-0000-0000-000000000000')]; refresh();
   const body=document.querySelector('#message-body'); body.value='kept';
   const expires=Number(document.querySelector('#organization-stream').dataset.sessionExpires);
   Date.now=()=>expires+1;
   window.draftWrites=0; const set=Storage.prototype.setItem;
   Storage.prototype.setItem=function(k,v){ if(k.startsWith('ribbitto.draft.v1:')) draftWrites++; return set.call(this,k,v); };
   body.dispatchEvent(new CustomEvent('message-sending',{bubbles:true,detail:{key:'00000000-0000-4000-8000-000000000001',body:'sent',inPlace:false}}));
   window.expiredSend={removed:keys.every(k=>localStorage.getItem(k)===null),text:body.value,writes:draftWrites};
  }`)
		if got := step(t, p, "read expired send result").MustEval(`() => expiredSend`); !got.Get("removed").Bool() || got.Get("writes").Int() != 0 || got.Get("text").Str() != "kept" {
			t.Fatalf("expired send: %s", got)
		}
	})
	for _, name := range []string{"switch-away", "channel-list", "persisted-pageshow", "message-sent", "notice-storage", "deadline-expiry", "draft-storage", "send-another-tab", "sidebar-sse", "sidebar-resync"} {
		t.Run(name, func(t *testing.T) {
			p := open(t, path, true)
			step(t, p, "seed topic draft").MustEval(`() => { window.written=put('02000000-0000-0000-0000-000000000000'); refresh(); }`)
			wait(t, p, "show seeded draft marker", `() => marked('`+topic+`') === true`)
			switch name {
			case "switch-away":
				step(t, p, "open stored draft").MustNavigate(server.URL + view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2}))
				step(t, p, "load stored draft").MustWaitLoad()
				wait(t, p, "restore without input", `() => document.querySelector('#message-body').value==='unfinished' && !marked('`+topic+`')`)
				step(t, p, "switch away from draft").MustNavigate(server.URL + path)
				step(t, p, "load after switching away").MustWaitLoad()
			case "channel-list":
				step(t, p, "open channel list").MustNavigate(server.URL + "/organizations/acme/channels")
				step(t, p, "load channel list").MustWaitLoad()
				wait(t, p, "list has no composer", `() => !document.querySelector('#message-composer') && marked('`+topic+`') === true`)
				wait(t, p, "second list marker starts hidden", `() => marked('channel-04000000000000000000000000000000') === false`)
				step(t, p, "write second list draft and refresh").MustEval(`() => { put('feed',{},null,null,'04000000-0000-0000-0000-000000000000'); refresh(); }`)
				wait(t, p, "show second list draft marker", `() => marked('channel-04000000000000000000000000000000') === true`)
				if got := step(t, p, "read channel list page errors").MustEval(`() => pageErrors`).Int(); got != 0 {
					t.Fatalf("channel list page errors: %d", got)
				}
			case "persisted-pageshow", "message-sent":
				step(t, p, "clear draft and refresh event").MustEval(`name => { localStorage.removeItem(written); if(name==='message-sent') document.querySelector('#message-body').dispatchEvent(new CustomEvent('message-sent',{bubbles:true,detail:{key:'unknown'}})); else refresh(); }`, name)
			case "notice-storage", "draft-storage", "send-another-tab":
				other := open(t, view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2}), false)
				if name == "send-another-tab" {
					wait(t, other, "restore draft in sending tab", `() => document.querySelector('#message-body').value==='unfinished'`)
					step(t, other, "send restored draft in other tab").MustEval(`() => document.querySelector('#message-composer').requestSubmit()`)
					wait(t, other, "send writes tombstone", `() => { const p=document.querySelector('#organization-stream').dataset; const k='ribbitto.draft.v1:'+p.member+':'+p.session+':01000000-0000-0000-0000-000000000000:02000000-0000-0000-0000-000000000000'; return JSON.parse(localStorage.getItem(k))?.text===''; }`)
				} else {
					step(t, other, "write cross-tab storage event").MustEval(`notice => { if(notice) localStorage.setItem('ribbitto.signed-out.v1','notice'); else put('02000000-0000-0000-0000-000000000000',{text:''}); }`, name == "notice-storage")
				}
			case "deadline-expiry":
				step(t, p, "fire page expiry").MustEval(`() => { elapsed=2*86400000; expireMarkers(); if (marked('topic-02000000000000000000000000000000') !== false || marked('channel-01000000000000000000000000000000') !== false) throw new Error("deadline did not hide marker immediately"); }`)
			case "sidebar-sse", "sidebar-resync":
				step(t, p, "remember old sidebar").MustEval(`() => window.previousSidebar=document.querySelector('#sidebar-channels')`)
				if name == "sidebar-sse" {
					if streams.publish(realtime.Outgoing{Ephemeral: true, Name: "sidebar", Data: []byte(renderIn(t, "en", view.SidebarLists(snapshot)))}) == 0 {
						t.Fatal("deliver sidebar frame: stream unavailable")
					}
				} else {
					step(t, p, "request sidebar resync").MustEval(`() => htmx.trigger(document.querySelector('#sidebar-resync'),'sidebar-resync')`)
				}
				wait(t, p, "sidebar replaced with marker reapplied", `() => document.querySelector('#sidebar-channels')!==previousSidebar && marked('`+topic+`') === true`)
			}
			shown := name == "switch-away" || name == "channel-list" || strings.HasPrefix(name, "sidebar-")
			wait(t, p, "final marker state for "+name, `() => marked('`+topic+`') === `+map[bool]string{true: "true", false: "false"}[shown]+` && marked('`+channel+`') === `+map[bool]string{true: "true", false: "false"}[shown])
		})
	}
}
