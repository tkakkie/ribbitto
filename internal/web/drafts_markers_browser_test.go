package web

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	frames := make(chan realtime.Outgoing, 1)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page := snapshot
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			for err := sender.Heartbeat(r.Context()); err == nil; {
				select {
				case out := <-frames:
					err = sender.Send(r.Context(), out)
				case <-r.Context().Done():
					return
				}
			}
		case strings.HasSuffix(r.URL.Path, "/resync"):
			templ.Handler(view.SidebarLists(page)).ServeHTTP(w, r)
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
			diagnostic := step(t, p.Context(context.Background()), "read marker failure state").MustEval(`() => JSON.stringify({url:location.href, storage:Object.entries(localStorage), markers:[...document.querySelectorAll('[data-draft-marker]')].map(m=>[m.closest('a').href,m.hidden])})`).Str()
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
	t.Run("row-predicate", func(t *testing.T) {
		p := open(t, path, true)
		source, err := fs.ReadFile(static.FS(), "message-drafts-v5.js")
		if err != nil {
			t.Fatal(err)
		}
		_, rest, ok := strings.Cut(string(source), "const marked = ")
		expression, _, _ := strings.Cut(rest, "\n")
		if !ok {
			t.Fatal("missing pure row predicate")
		}
		got := step(t, p, "unit-test pure row predicate").MustEval(`() => { const marked = ` + expression + `
          const places=new Set(['channel','channel:topic']);
          return JSON.stringify([marked(places,'channel','feed',false),marked(places,'channel','topic',false),marked(places,'channel','sibling',false),marked(places,'channel','topic',true),marked(places,'other','feed',false)]); }`).Str()
		if got != "[true,true,false,false,false]" {
			t.Fatalf("pure row predicate: %s", got)
		}
	})
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
		{"open-conversation", `put('03000000-0000-0000-0000-000000000000')`, `marked('` + channel + `') && !marked('` + sibling + `')`},
		{"page-deadline", `put(); elapsed=2*86400000`, `marked('` + channel + `') === false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := open(t, path, true)
			step(t, p, "seed isolated marker predicate").MustEval(`() => { ` + tc.seed + `; refresh(); }`)
			wait(t, p, tc.name, `() => `+tc.want)
			link := step(t, p, "find accessible channel row").MustElement("#link-" + channel)
			tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: link.Object.ObjectID}).Call(step(t, p, "read accessible channel name"))
			if err != nil || len(tree.Nodes) == 0 || tree.Nodes[0].Name == nil {
				t.Fatalf("channel accessible name: tree=%+v, error=%v", tree, err)
			}
			shown := step(t, p, "read marker visibility for accessible name").MustEval(`() => marked('` + channel + `')`).Bool()
			if got := tree.Nodes[0].Name.Value.Str(); strings.Contains(got, "Draft") != shown {
				t.Fatalf("accessible name %q, marker shown=%t", got, shown)
			}
		})
	}
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
					select {
					case frames <- realtime.Outgoing{Ephemeral: true, Name: "sidebar", Data: []byte(renderIn(t, "en", view.SidebarLists(snapshot)))}:
					case <-time.After(3 * time.Second):
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
