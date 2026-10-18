package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// Capture only the stream's jitter. Advancing it is synchronous, so a missing
// guard causes a real navigation instead of passing a no-reload timeout.
func resetClock(page *rod.Page) {
	page.MustEvalOnNewDocument(`window.name=String(Number(window.name)+1)`)
	page.MustEval(`() => {
  window.name='1'; window.reloadTimers=new Map(); let id=-1;
  const schedule=window.setTimeout, cancel=window.clearTimeout;
  window.setTimeout=(fn,ms,...args)=>{
   if(!new Error().stack.includes('message-stream-v13.js')) return schedule(fn,ms,...args);
   if(ms<0 || ms>5000) throw new Error('reset jitter out of range');
   window.reloadTimers.set(id,()=>fn(...args)); return id--;
  };
  window.clearTimeout=id=>{window.reloadTimers.delete(id);cancel(id);};
  window.fireReset=()=>{const work=[...window.reloadTimers.values()];window.reloadTimers.clear();work.forEach(fn=>fn());};
 }`)
}

func resetHeld(t *testing.T, page *rod.Page) {
	t.Helper()
	if !page.MustEval(`() => window.name==='1' && window.reloadTimers.size===0 &&
  !document.getElementById('stream-stopped').hidden &&
  document.getElementById('stream-stopped').getAttribute('role')==='status' &&
  document.querySelector('#stream-stopped a').getAttribute('href')===document.getElementById('message-composer').getAttribute('action')`).Bool() {
		t.Fatal("reset did not hold with a status and conversation reload link")
	}
	page.MustEval(`() => window.fireReset()`)
	if page.MustEval(`() => window.name`).Str() != "1" {
		t.Fatal("held reset reloaded")
	}
}

func resetReload(t *testing.T, page *rod.Page) {
	t.Helper()
	page.MustWait(`() => window.reloadTimers.size>0`)
	if page.MustEval(`() => window.reloadTimers.size`).Int() != 1 {
		t.Fatal("reset scheduled more than one timer")
	}
	page.MustEval(`() => window.fireReset()`)
	page.MustWait(`() => window.name==='2' && !!window.source`)
}

func testStreamResetBrowser(t *testing.T, browser *rod.Browser) {
	// Isolate each predicate: another at-stake state must not mask its removal.
	for _, test := range []struct {
		state string
		holds bool
	}{
		{"request", true},
		{"alert-unconfirmed", true},
		{"alert-notSent", true},
		{"alert-stillUnconfirmed", true},
		{"alert-busy", false},
		{"alert-capacity", false},
		{"alert-recovered", false},
		{"alert-repeat", false},
		{"alert-other", false},
		{"queued", true},
		{"sending", true},
		{"failed", true},
		{"unconfirmed", true},
		{"confirmed", false},
		{"draft", false},
	} {
		t.Run(test.state, func(t *testing.T) {
			page, events, _, _ := pendingBrowser(t, browser, false)
			resetClock(page)
			page.MustEval(`state=>{
    const form=document.getElementById('message-composer');
    if(state==='request') form.classList.add('htmx-request');
    else if(state.startsWith('alert-')) {
     const alert=document.getElementById('message-send-alert');
     alert.textContent=state==='alert-other'?'arbitrary text':alert.dataset[state.slice(6)];
    }
    else if(state==='draft') form.elements.body.value='draft';
    else {
     const item=document.getElementById('message-pending-template').content.firstElementChild.cloneNode(true);
     item.querySelector('form').dataset.state=state==='unconfirmed'?'failed':state;
     if(state==='unconfirmed') item.querySelector('form').dataset.unconfirmed='true';
     document.getElementById('message-pending').append(item);
    }
   }`, test.state)
			events <- realtime.Outgoing{Name: "reset"}
			page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
			if test.holds {
				resetHeld(t, page)
				page.MustEval(`() => {
     document.getElementById('message-composer').classList.remove('htmx-request');
     document.getElementById('message-send-alert').firstChild?.replaceData(0,100,'');
     document.querySelector('#message-pending form')?.setAttribute('data-state','confirmed');
    }`)
			}
			resetReload(t, page)
		})
	}
	t.Run("single-cancel-recheck", func(t *testing.T) {
		page, events, _, _ := pendingBrowser(t, browser, false)
		resetClock(page)
		events <- realtime.Outgoing{Name: "reset"}
		page.MustWait(`() => window.reloadTimers.size===1`)
		// Repeated changes with nothing at stake must keep the original timer.
		page.MustEval(`() => {window.timer=[...window.reloadTimers.keys()][0];document.getElementById('message-composer').classList.add('test');}`)
		page.MustEval(`() => document.getElementById('message-composer').classList.remove('test')`)
		if !page.MustEval(`() => window.reloadTimers.size===1 && window.reloadTimers.has(window.timer)`).Bool() {
			t.Fatal("mutation replaced or duplicated the timer")
		}
		// The callback runs in the mutation's task, before MutationObserver delivery.
		page.MustEval(`() => {document.getElementById('message-composer').classList.add('htmx-request');window.fireReset();}`)
		resetHeld(t, page)
		page.MustEval(`() => document.getElementById('message-composer').classList.remove('htmx-request')`)
		page.MustWait(`() => window.reloadTimers.size===1`)
		page.MustEval(`() => document.getElementById('message-composer').classList.add('htmx-request')`)
		resetHeld(t, page)
		page.MustEval(`() => document.getElementById('message-composer').classList.remove('htmx-request')`)
		resetReload(t, page)
	})
	for _, ordinary := range []bool{false, true} {
		for _, timing := range []string{"held", "jitter", "failed"} {
			for _, settle := range []string{"success", "retry", "remove", "edit"} {
				if timing == "failed" && settle == "success" || ordinary && (settle == "remove" || settle == "edit") {
					continue
				}
				t.Run(fmt.Sprintf("ordinary=%t/%s/%s", ordinary, timing, settle), func(t *testing.T) {
					page, events, posts, _ := pendingBrowser(t, browser, false)
					resetClock(page)
					if ordinary {
						page.MustEval(`() => document.getElementById('message-pending-template').remove()`)
					}
					reset := func() {
						events <- realtime.Outgoing{Name: "reset"}
						page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
					}
					if timing == "jitter" {
						reset()
						page.MustWait(`() => window.reloadTimers.size===1`)
					}
					page.MustEval(`() => {const b=document.getElementById('message-body');b.value='kept';b.form.requestSubmit();}`)
					request := receivePending(t, page, posts)
					if timing == "held" {
						reset()
					}
					if timing != "failed" {
						resetHeld(t, page)
					}
					if settle == "success" {
						request.reply <- pendingAnswer(t, kernel.ID{51})
					} else {
						request.reply <- browserReply{status: 400, body: []byte("refused")}
						page.MustWait(`ordinary => ordinary ? !!document.getElementById('message-send-alert').textContent && !document.querySelector('#message-composer.htmx-request') : document.querySelector('[data-pending-form]').dataset.state==='failed'`, ordinary)
						if timing == "failed" {
							reset()
						}
						resetHeld(t, page)
						if !page.MustEval(`ordinary => ordinary ? document.getElementById('message-body').value==='kept' : document.querySelector('[data-pending-body]').textContent==='kept'`, ordinary).Bool() {
							t.Fatal("failure lost text")
						}
						switch settle {
						case "retry":
							page.MustEval(`ordinary => (ordinary?document.getElementById('message-composer'):document.querySelector('[data-pending-form]')).requestSubmit()`, ordinary)
							retry := receivePending(t, page, posts)
							resetHeld(t, page)
							retry.reply <- pendingAnswer(t, kernel.ID{51})
						case "remove", "edit":
							page.MustElement("[data-pending-" + settle + "]").MustClick()
							if settle == "edit" && page.MustEval(`() => document.getElementById('message-body').value`).Str() != "kept" {
								t.Fatal("Edit did not restore draft")
							}
						}
					}
					resetReload(t, page)
				})
			}
		}
	}
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue/lost=%t", lost), func(t *testing.T) {
			page, events, posts, _ := pendingBrowser(t, browser, false)
			resetClock(page)
			if lost {
				var intercepted atomic.Bool
				router := page.HijackRequests()
				router.MustAdd("*/organizations/acme/channels/*", func(h *rod.Hijack) {
					if h.Request.Method() != "POST" || strings.HasSuffix(h.Request.URL().Path, "/read") || strings.HasSuffix(h.Request.URL().Path, "/typing") {
						h.ContinueRequest(&proto.FetchContinueRequest{})
						return
					}
					if intercepted.Swap(true) {
						h.ContinueRequest(&proto.FetchContinueRequest{})
						return
					}
					if err := h.LoadResponse(http.DefaultClient, true); err != nil {
						t.Error(err)
					}
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				})
				go router.Run()
				t.Cleanup(func() { _ = router.Stop() })
			}
			page.MustEval(`() => {const b=document.getElementById('message-body');for(let i=0;i<3;i++){b.value='queued '+i;b.form.requestSubmit();}}`)
			first := receivePending(t, page, posts)
			events <- realtime.Outgoing{Name: "reset"}
			page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
			resetHeld(t, page)
			first.reply <- pendingAnswer(t, kernel.ID{51})
			if lost {
				page.MustWait(`() => document.querySelector('[data-pending-form]').dataset.state==='failed'`)
				resetHeld(t, page)
				// The unknown first outcome pauses the queue with both later texts kept.
				if len(posts) != 0 || !page.MustEval(`() => { const f=document.querySelector('[data-pending-form]'), a=document.getElementById('message-send-alert');
 return f.dataset.unconfirmed==='true' && f.querySelector('[role=status]').textContent===a.dataset.unconfirmed &&
  document.querySelectorAll('[data-state=queued]').length===2;
}`).Bool() {
					t.Fatal("uncertain outcome did not pause queue")
				}
				page.MustEval(`() => document.querySelector('[data-pending-remove]').click()`)
			}
			for i := 1; i < 3; i++ {
				next := receivePending(t, page, posts)
				if next.values.Get("body") != fmt.Sprintf("queued %d", i) {
					t.Fatal("queue changed order or text")
				}
				resetHeld(t, page)
				next.reply <- pendingAnswer(t, kernel.ID{byte(51 + i)})
			}
			resetReload(t, page)
		})
	}
}
