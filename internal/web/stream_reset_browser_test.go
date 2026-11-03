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
   if(!/message-stream-[A-Z0-9]{8}\.js/.test(new Error().stack)) return schedule(fn,ms,...args);
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
	mustWaitAcrossNavigation(t, page, `() => window.name==='2' && !!window.source`)
}

func testStreamResetBrowser(t *testing.T, browser *rod.Browser) {
	for _, delayed := range []string{"composer-", "message-stream-"} {
		t.Run("handshake/late-"+delayed, func(t *testing.T) {
			page, events, _, _ := pendingBrowser(t, browser, false, delayed)
			resetClock(page)
			load := func() {
				t.Helper()
				page.MustEval(`part => new Promise((resolve,reject)=>{
 const script=document.createElement('script');
 script.src=[...document.scripts].find(s=>s.src.includes(part)).src+'?late=1';
 script.onload=()=>resolve(true);script.onerror=()=>reject(new Error("script load failed"));document.head.append(script);
})`, delayed)
			}
			queue := func() {
				page.MustEval(`() => {
 document.addEventListener('htmx:beforeRequest',e=>{e.preventDefault();e.stopImmediatePropagation()},{capture:true,once:true});
 const b=document.getElementById('message-body');b.value='queued before reload';b.form.requestSubmit();
}`)
				page.MustWait(`() => !!document.querySelector('[data-state=queued]')`)
			}
			if delayed == "composer-" {
				events <- realtime.Outgoing{Name: "reset"}
				page.MustWait(`() => window.reloadTimers.size===1`)
				page.MustEval(`() => {window.startup=[];document.addEventListener('message-pending-changed',e=>startup.push(e.detail.holdsReload))}`)
				load()
				if page.MustEval(`() => JSON.stringify(startup)`).Str() != "[false]" {
					t.Fatal("late composer did not publish its initial reload predicate")
				}
				queue()
			} else {
				queue()
				load()
				page.MustEval(`() => document.getElementById("organization-stream").dispatchEvent(new CustomEvent("htmx:sseOpen",{bubbles:true,detail:{source:window.source}}))`)
				events <- realtime.Outgoing{Name: "reset"}
				page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
			}
			resetHeld(t, page)
			page.MustEval(`() => document.querySelector('[data-pending-remove]').click()`)
			resetReload(t, page)
		})
	}

	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("reload-then-notify/held=%t", held), func(t *testing.T) {
			page, events, posts, _ := pendingBrowser(t, browser, false)
			resetClock(page)
			page.MustEval(`() => {const b=document.getElementById('message-body');b.value='confirmed';b.form.requestSubmit()}`)
			receivePending(t, page, posts).reply <- pendingAnswer(t, kernel.ID{51})
			page.MustWait(`() => window.answers===1`)
			events <- realtime.Outgoing{Name: "reset"}
			page.MustWait(`() => window.reloadTimers.size===1`)
			// Keep the old document alive while its reload navigation is in flight.
			release := make(chan struct{})
			router := page.HijackRequests()
			router.MustAdd("*", func(h *rod.Hijack) {
				if h.Request.Type() == proto.NetworkResourceTypeDocument {
					<-release
					h.Response.Fail(proto.NetworkErrorReasonAborted)
					return
				}
				h.ContinueRequest(&proto.FetchContinueRequest{})
			})
			go router.Run()
			t.Cleanup(func() { close(release); _ = router.Stop() })
			if !page.MustEval(`held => {
 // Removal queues a recheck before the timer fires in this same task.
 document.querySelector('[data-pending-remove]').click();
 window.fireReset();
 // A later notification must also be inert throughout the navigation.
 if(held){
  document.addEventListener('htmx:beforeRequest',e=>{e.preventDefault();e.stopImmediatePropagation()},{capture:true,once:true});
  const b=document.getElementById('message-body');b.value='next';b.form.requestSubmit();
 } else document.dispatchEvent(new CustomEvent('message-pending-query',{detail:{}}));
 return Promise.resolve().then(()=>reloadTimers.size===0 && document.getElementById('stream-stopped').hidden);
}`, held).Bool() {
				t.Fatal("queue notification after reload rearmed a timer or revealed the notice")
			}
		})
	}

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
			page, events, posts, _ := pendingBrowser(t, browser, false)
			resetClock(page)
			page.MustEval(`state=>{
    const form=document.getElementById('message-composer');
    if(state==='request') form.classList.add('htmx-request');
    else if(state.startsWith('alert-')) {
     const alert=document.getElementById('message-send-alert');
     alert.textContent=state==='alert-other'?'arbitrary text':alert.dataset[state.slice(6)];
    }
    else if(state==='draft') form.elements.body.value='draft';
   }`, test.state)
			if test.state == "queued" || test.state == "sending" || test.state == "failed" || test.state == "unconfirmed" || test.state == "confirmed" {
				if test.state == "queued" {
					page.MustEval(`() => document.addEventListener('htmx:beforeRequest',e=>{e.preventDefault();e.stopImmediatePropagation()},{capture:true,once:true})`)
				}
				page.MustEval(`() => {const b=document.getElementById('message-body');b.value='pending';b.form.requestSubmit()}`)
				if test.state != "queued" {
					request := receivePending(t, page, posts)
					if test.state != "sending" {
						reply := browserReply{status: 400, body: []byte("refused")}
						if test.state == "unconfirmed" {
							reply.status = 500
						}
						if test.state == "confirmed" {
							reply = pendingAnswer(t, kernel.ID{51})
						}
						request.reply <- reply
						page.MustWait(`() => window.answers===1`)
					}
				}
			}
			events <- realtime.Outgoing{Name: "reset"}
			page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
			if test.holds {
				resetHeld(t, page)
				page.MustEval(`() => {
     document.getElementById('message-composer').classList.remove('htmx-request');
     document.getElementById('message-send-alert').firstChild?.replaceData(0,100,'');
     document.querySelector('[data-pending-remove]')?.click();
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
	for _, state := range []string{"queued", "sending", "failed", "unconfirmed"} {
		for _, action := range []string{"retry", "edit", "remove"} {
			t.Run("output-only/"+state+"/"+action, func(t *testing.T) {
				page, events, posts, _ := pendingBrowser(t, browser, false)
				resetClock(page)
				page.MustEval(`() => {const b=document.getElementById('message-body');for(const text of ['first','second']){b.value=text;b.form.requestSubmit()}}`)
				first := receivePending(t, page, posts)
				if state == "failed" || state == "unconfirmed" {
					status := 400
					if state == "unconfirmed" {
						status = 500
					}
					first.reply <- browserReply{status: status, body: []byte("refused")}
					page.MustWait(`() => window.answers===1`)
				}
				// Corrupt every projection before reset, without notifying the queue.
				if !page.MustEval(`state => {
 const forms=[...document.querySelectorAll('[data-pending-form]')];
 window.subject=forms[state==='queued'?1:0];
 for(const f of forms){f.dataset.state='confirmed';f.dataset.unconfirmed=f.hasAttribute('data-unconfirmed')?'':'true'}
 const b=document.getElementById('message-body');b.dispatchEvent(new Event('input',{bubbles:true}));
 return subject.querySelector('[data-pending-edit]').disabled===(state==='queued'||state==='sending') && !subject.querySelector('[data-pending-remove]').disabled;
}`, state).Bool() {
					t.Fatal("external projections changed action availability")
				}
				events <- realtime.Outgoing{Name: "reset"}
				page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
				resetHeld(t, page)
				page.MustEval(`action => {
 if(action==='retry')subject.requestSubmit();
 else {const button=subject.querySelector('[data-pending-'+action+']');button.disabled=false;button.click()}
}`, action)
				failed := state == "failed" || state == "unconfirmed"
				if action == "edit" && !page.MustEval(`failed => document.getElementById('message-body').value===(failed?'first':'')`, failed).Bool() {
					t.Fatal("external projections changed Edit eligibility or text")
				}
				if state == "queued" || state == "sending" {
					first.reply <- pendingAnswer(t, kernel.ID{51})
				}
				if state == "unconfirmed" && action == "retry" {
					retry := receivePending(t, page, posts)
					if retry.values.Encode() != first.values.Encode() {
						t.Fatal("unconfirmed Retry lost its place or identity")
					}
					retry.reply <- pendingAnswer(t, kernel.ID{51})
				}
				if state != "queued" || action != "remove" {
					next := receivePending(t, page, posts)
					if next.values.Get("body") != "second" {
						t.Fatal("external projections changed FIFO dispatch")
					}
					resetHeld(t, page)
					next.reply <- pendingAnswer(t, kernel.ID{52})
				}
				if state == "failed" && action == "retry" {
					retry := receivePending(t, page, posts)
					if retry.values.Encode() != first.values.Encode() {
						t.Fatal("refused Retry changed identity")
					}
					retry.reply <- pendingAnswer(t, kernel.ID{51})
				}
				resetReload(t, page)
			})
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
