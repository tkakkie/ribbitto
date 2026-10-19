package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testSendKeyBrowser(t *testing.T, browser *rod.Browser) {
	for _, width := range []int{1280, 390} {
		for _, mode := range []identity.SendKey{identity.SendKeyEnter, identity.SendKeyModifierEnter} {
			cases := []string{"newline", "shift", "ctrl", "meta", "swap"}
			for _, guard := range []string{"isComposing", "keyCode", "recentEnd"} {
				for _, key := range []string{"enter", "ctrl", "meta"} {
					cases = append(cases, guard+"/"+key)
				}
			}
			for _, name := range cases {
				t.Run(fmt.Sprintf("%d/%s/%s", width, mode, name), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
					defer cancel()
					handler, _, _, place := composerHandler(t, false, func(s *Services) { s.Sessions = sendKeySession{key: mode} })
					other := identity.SendKeyModifierEnter
					if mode == other {
						other = identity.SendKeyEnter
					}
					swapped, _, _, _ := composerHandler(t, false, func(s *Services) { s.Sessions = sendKeySession{key: other} })
					var posts atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
						if r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/typing") {
							posts.Add(1)
							if name == "swap" {
								swapped.ServeHTTP(w, r)
								return
							}
						}
						handler.ServeHTTP(w, r)
					}))
					defer server.Close()
					var page *rod.Page
					createCtx, stopCreate := context.WithTimeout(ctx, 10*time.Second)
					runBounded(t, createCtx, time.Now(), "setup: create send-key page", func() { page = browser.Context(ctx).MustPage() })
					stopCreate()
					defer func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() }()
					step := pageStep(ctx, page)
					step(t, "setup: clear drafts, count submits and load selected mode", func(p *rod.Page) {
						p.MustSetViewport(width, 844, 1, false)
						p.MustEvalOnNewDocument(`(() => { localStorage.clear(); window.submits=0;
       document.addEventListener('submit',e=>{if(e.target.id==='message-composer') window.submits++;},true);
       document.addEventListener('htmx:afterSwap',e=>{if(e.target.id==='message-composer-state') window.swaps=(window.swaps||0)+1;}); })()`)
						p.MustNavigate(server.URL + view.ChannelURL("acme", place.Channel)).MustWaitLoad()
						p.MustWait(`mode => document.getElementById('message-composer-state').dataset.sendKey===mode && document.getElementById('message-body').enterKeyHint===(mode==='enter'?'send':'enter')`, mode)
					})
					if name == "swap" {
						step(t, "swap: other-mode 422 changes state, alert and key label while preserving text", func(p *rod.Page) {
							p.MustEval(`() => { const b=document.getElementById('message-body'); b.value='\n'; window.keptBody=b; b.form.requestSubmit(); }`)
							p.MustWait(`mode => window.swaps===1 && document.getElementById('message-composer-state').dataset.sendKey===mode && document.getElementById('message-body').enterKeyHint===(mode==='enter'?'send':'enter') && document.querySelector('#message-help[role=alert]')?.textContent==='Enter 1–4000 characters without control characters or directional embeddings and overrides.' && document.getElementById('message-body')===window.keptBody && window.keptBody.value==='\n'`, other)
						})
						if posts.Load() != 1 {
							t.Fatal("swap: expected exactly one POST")
						}
						step(t, "after swap: next Enter uses the new mode", func(p *rod.Page) {
							p.MustElement("#message-body").MustSelectAllText().MustInput("after swap")
							p.MustElement("#message-body").MustType(input.Enter)
							if other == identity.SendKeyModifierEnter {
								p.MustWait(`() => window.submits===1 && document.getElementById('message-body').value==='after swap\n'`)
							} else {
								p.MustWait(`() => window.submits===2 && window.swaps===2`)
							}
						})
						return
					}
					step(t, "input: prepare unique draft without a POST", func(p *rod.Page) { p.MustElement("#message-body").MustInput("draft " + name) })
					if name == "newline" || name == "shift" {
						step(t, "plain Enter: selected mode sends or inserts a newline", func(p *rod.Page) {
							if name == "shift" {
								p.KeyActions().Press(input.ShiftLeft).Type(input.Enter).Release(input.ShiftLeft).MustDo()
							} else {
								p.MustElement("#message-body").MustType(input.Enter)
							}
							if mode == identity.SendKeyModifierEnter || name == "shift" {
								p.MustWait(`() => document.getElementById('message-body').value.endsWith('\n') && window.submits===0`)
							} else {
								p.MustWait(`() => window.submits===1 && window.swaps===1`)
							}
						})
						if mode == identity.SendKeyEnter && name == "newline" {
							if posts.Load() != 1 {
								t.Fatal("Enter mode: expected one POST")
							}
							return
						}
					} else if strings.Contains(name, "/") {
						parts := strings.Split(name, "/")
						step(t, "IME guard alone: key must cause zero submissions", func(p *rod.Page) {
							p.MustEval(`(guard,key) => { const b=document.getElementById('message-body');
        if(guard==='recentEnd') b.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}));
        window.endedAt=performance.now();
        b.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true,isComposing:guard==='isComposing',keyCode:guard==='keyCode'?229:13,ctrlKey:key==='ctrl',metaKey:key==='meta'}));
       }`, parts[0], parts[1])
							p.MustWait(`() => performance.now()-window.endedAt>=120`)
							if p.MustEval(`() => window.submits`).Int() != 0 {
								t.Fatal("IME guard: expected zero submissions")
							}
						})
					}
					if posts.Load() != 0 {
						t.Fatal("guard/newline: expected zero POSTs")
					}
					step(t, "deliberate send after IME window: exactly one submission and completed POST", func(p *rod.Page) {
						p.MustEval(`(mode,key) => document.getElementById('message-body').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true,ctrlKey:key==='ctrl'||(mode==='modifier_enter'&&key!=='meta'),metaKey:key==='meta'}))`, mode, name)
						p.MustWait(`() => window.submits===1 && window.swaps===1`)
					})
					if posts.Load() != 1 {
						t.Fatalf("deliberate send: got %d POSTs, want one", posts.Load())
					}
				})
			}
		}
	}
}
