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
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type noticeVisit struct{ dividerVisit }

func (v *noticeVisit) Page(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := v.dividerVisit.Page(ctx, m, channel, topic, before)
	// Layout navigations reuse the same snapshot independently of previous POSTs.
	page.FeedFirstUnread, page.TopicFirstUnread = v.first, v.first
	return page, err
}

func (v *noticeVisit) Around(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, _ int64) (conversation.ChannelPage, error) {
	page, err := v.Page(ctx, m, channel, topic, nil)
	page.Entries = nil
	for seq := 5; seq <= 54; seq++ {
		page.Entries = append(page.Entries, conversation.Entry{Message: conversation.Message{ID: kernel.ID{byte(seq)}, TopicID: kernel.ID{5}, EventSeq: int64(seq), Body: fmt.Sprintf("Entry %d", seq)}})
	}
	return page, err
}

func testUnreadAboveBrowser(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		for _, lang := range []string{"en", "ja"} {
			for _, scripts := range []bool{true, false} {
				t.Run(fmt.Sprintf("topic=%t/%s/scripts=%t", topic, lang, scripts), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					visit := &noticeVisit{dividerVisit: dividerVisit{first: 30}}
					handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit })
					var reads atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.HasSuffix(r.URL.Path, "/read") {
							reads.Add(1)
						}
						r.Header.Set("Accept-Language", lang)
						r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
						handler.ServeHTTP(w, r)
					}))
					defer server.Close()
					path := view.ChannelURL("acme", kernel.ID{1})
					if topic {
						path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
					}
					page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
					defer page.MustClose()
					if err := (proto.EmulationSetEmulatedVisionDeficiency{Type: proto.EmulationSetEmulatedVisionDeficiencyTypeDeuteranopia}).Call(page); err != nil {
						t.Fatal(err)
					}
					if !scripts {
						if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
							t.Fatal(err)
						}
					}
					if scripts {
						page.MustEvalOnNewDocument(`(() => {
 const prop=Object.getOwnPropertyDescriptor(Element.prototype,'scrollTop');
 window.listWrites=0;
 document.addEventListener('htmx:afterRequest', e => {
  if(e.detail.elt?.id==='read-form' && e.detail.successful) document.documentElement.dataset.readComplete='true';
 });
 document.addEventListener('htmx:afterSettle', e => {
  if(e.detail.target?.id==='message-items') document.documentElement.dataset.historySettled='true';
 });
 Object.defineProperty(Element.prototype,'scrollTop',{...prop,set(v) {
  if(this.id==='message-list') window.listWrites++;
  prop.set.call(this,v);
 }});
})()`)
					}
					for _, size := range [][2]int{{1280, 800}, {390, 844}, {1280, 400}, {1280, 200}} {
						visit.read.Store(false)
						page.MustSetViewport(size[0], size[1], 1, false)
						page.MustNavigate(server.URL + path).MustWaitLoad()
						for _, zoom := range []int{100, 200} {
							page.MustEval(`z => document.documentElement.style.fontSize = z + '%'`, zoom)
							if !page.MustEval(`() => {
     const a=document.querySelector('main > div > a[href$="#unread-divider"]'), n=a.parentElement, b=n.getBoundingClientRect(), l=document.querySelector('#message-list').getBoundingClientRect(), r=a.getBoundingClientRect();
     return b.top>=0 && b.bottom<=innerHeight && b.bottom<=l.top && document.documentElement.scrollWidth<=innerWidth && r.width>=44 && r.height>=(innerWidth<852?44:24);
    }`).Bool() {
								t.Fatalf("notice layout or target size %v zoom=%d: %s", size, zoom, page.MustEval(`() => { const a=document.querySelector('main > div > a[href$="#unread-divider"]'); return JSON.stringify({notice:a.parentElement.getBoundingClientRect(),link:a.getBoundingClientRect(),list:document.querySelector('#message-list').getBoundingClientRect(),width:document.documentElement.scrollWidth,height:innerHeight}); }`).Str())
							}
						}
						page.MustEval(`() => document.documentElement.style.fontSize = ''`)
					}
					for _, size := range [][2]int{{1280, 800}, {390, 844}, {1280, 200}} {
						page.MustSetViewport(size[0], size[1], 1, false)
						visit.read.Store(false)
						page.MustNavigate(server.URL + path).MustWaitLoad()
						if scripts {
							page.MustWait(`() => document.documentElement.dataset.readComplete === 'true'`)
							page.MustElement("#load-older a").MustClick()
							page.MustWait(`() => !!document.querySelector('#unread-divider') && document.documentElement.dataset.historySettled === 'true'`)
							if !page.MustEval(`() => document.querySelector('main > div > a[href$="#unread-divider"]') !== null && document.querySelector('#message-status').textContent === '' && !document.activeElement.closest('main > div > a[href$="#unread-divider"]')`).Bool() {
								t.Fatal("prepend removed or announced notice")
							}
						}
						page.MustEval(`() => { const controls=[...document.querySelectorAll('main > header a, main > header button')].filter(e=>e.getClientRects().length); controls.at(-1).focus(); }`)
						page.Keyboard.MustType(input.Tab)
						if !page.MustEval(`() => { const a=document.activeElement; return a.matches('a[href$="#unread-divider"]') && getComputedStyle(a).outlineStyle==='solid' && parseFloat(getComputedStyle(a).outlineWidth)>=2; }`).Bool() {
							t.Fatal("header to jump Tab order or focus outline")
						}
						page.Keyboard.MustType(input.Tab)
						if page.MustEval(`() => document.activeElement.id`).Str() != "message-list" {
							t.Fatal("jump to list Tab order")
						}
						page.KeyActions().Press(input.ShiftLeft).Type(input.Tab).Release(input.ShiftLeft).MustDo()
						before := reads.Load()
						page.Keyboard.MustType(input.Enter)
						page.MustWaitLoad()
						page.MustWait(`() => location.hash === '#unread-divider'`)
						if scripts {
							page.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
							if writes := page.MustEval(`() => window.listWrites`).Int(); writes != 0 {
								t.Fatalf("composer placement: %d scroll writes on fragment load; stop without changing composer", writes)
							}
						}
						if !page.MustEval(`() => {
   const d=document.querySelector('#unread-divider'), l=document.querySelector('#message-list'), b=d.getBoundingClientRect(), r=l.getBoundingClientRect();
   return b.top>=Math.max(0,r.top) && b.bottom<=Math.min(innerHeight,r.bottom) && l.scrollTop+l.clientHeight<l.scrollHeight-1 && d.nextElementSibling.dataset.eventSeq==='30';
  }`).Bool() {
							t.Fatal("composer placement: divider not visible away from newest; stop without changing composer")
						}
						if page.MustEval(`() => !!document.querySelector('a[href$="#unread-divider"]') || !!document.querySelector('#read-form')`).Bool() {
							t.Fatal("destination notice or reading form")
						}
						active := page.MustEval(`() => document.activeElement.tagName + '#' + document.activeElement.id`).Str()
						page.Keyboard.MustType(input.Tab)
						next := page.MustEval(`() => { const a=document.activeElement; return a.closest('#message-items > li')?.dataset.eventSeq + ':' + a.tagName; }`).Str()
						t.Logf("fragment focus=%s; next Tab=%s", active, next)
						if next != "30:A" || !page.MustEval(`() => document.activeElement === [...document.querySelector('#unread-divider').nextElementSibling.querySelectorAll('a[href],button,input:not([type=hidden])')].find(e => e.getClientRects().length && !e.disabled)`).Bool() {
							t.Fatalf("unusable next Tab after fragment: %s", next)
						}
						if reads.Load() != before {
							t.Fatal("jump added a reading POST")
						}
					}
				})
			}
		}
	}
}
