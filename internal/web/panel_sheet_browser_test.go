package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// sheetVisit keeps something unread for every load and serves the snapshot
// cursor the test sets, which stands for the messages a later load includes.
type sheetVisit struct {
	readControlVisit
	cursor atomic.Int64
}

func (v *sheetVisit) Page(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := v.fakeMessages.Page(ctx, m, channel, topic, before)
	cursor := v.cursor.Load()
	page.FeedFirstUnread, page.TopicFirstUnread, page.EventCursor = 7, 8, &cursor
	return page, err
}

// Below the wide breakpoint an open panel covers the conversation, as a
// bottom sheet below desk: automatic reading waits for it, Mark as read
// does not, and the sheet keeps focus and scrolling to itself (#972).
func testPanelSheetBrowser(t *testing.T, browser *rod.Browser) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range []bool{false, true} {
		t.Run(fmt.Sprintf("topic=%t", topic), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			visit := &sheetVisit{}
			visit.cursor.Store(42)
			// Enough messages that the conversation behind could scroll.
			for i := range 40 {
				visit.entries = append(visit.entries, conversation.Entry{Message: conversation.Message{ID: kernel.ID{byte(i + 1)}, ChannelID: kernel.ID{1}, TopicID: kernel.ID{5}, EventSeq: int64(i + 1), Body: "message", CreatedAt: time.Now()}, Handle: "author"})
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit }))
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var reads []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/read") && r.ParseForm() == nil {
					cursor := r.PostForm.Get("cursor")
					if r.Header.Get("HX-Request") == "" {
						cursor += " plain"
					}
					mu.Lock()
					reads = append(reads, cursor)
					mu.Unlock()
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			path := view.ChannelURL("acme", kernel.ID{1})
			if topic {
				path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
			}
			// read waits at least 200 ms for exactly these reading POSTs.
			read := func(want string) {
				t.Helper()
				got := ""
				for i := 0; i < 100 && (i < 10 || got != want); i++ {
					time.Sleep(20 * time.Millisecond)
					mu.Lock()
					got = strings.Join(reads, ",")
					mu.Unlock()
				}
				if got != want {
					t.Fatalf("reading POSTs %q, want %q", got, want)
				}
			}
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			arrive := func(cursor string) {
				page.MustEval(`cursor => { document.getElementById('organization-stream').dataset.appliedCursor = cursor; document.dispatchEvent(new Event('read-applied')); }`, cursor)
			}
			load := func(what string, action func(), want string) {
				t.Helper()
				wait := page.Timeout(10 * time.Second).WaitNavigation(proto.PageLifecycleEventNameLoad)
				action()
				wait()
				if err := page.Timeout(5 * time.Second).Wait(rod.Eval(want)); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
			}
			const opened = `() => document.activeElement.id === 'panel-heading' && document.getElementById('conversation').inert`
			const closed = `() => !document.getElementById('side-panel') && document.activeElement.id === 'panel-control-overview' && !document.getElementById('conversation').inert`
			const sheet = `() => {
				const width = document.documentElement.clientWidth, height = document.documentElement.clientHeight;
				const panel = document.getElementById('side-panel'), box = panel.getBoundingClientRect(), style = getComputedStyle(panel);
				const dim = document.getElementById('panel-backdrop')?.getBoundingClientRect() ?? {};
				if (style.position !== 'fixed' || box.left !== 0 || box.right !== width || Math.abs(box.bottom - height) > 0.5) return 'sheet not along the bottom: ' + JSON.stringify(box);
				if (style.borderTopLeftRadius !== '12px' || style.borderTopRightRadius !== '12px' || style.borderBottomLeftRadius !== '0px') return 'sheet corners';
				if (box.height > 0.8 * height + 0.5) return 'sheet taller than 80 %: ' + box.height + ' of ' + height;
				if (dim.left !== 0 || dim.top !== 0 || dim.right < width || dim.bottom < height) return 'conversation not dimmed';
				if (document.documentElement.scrollWidth > width || panel.scrollWidth > panel.clientWidth) return 'sideways scroll';
				return '';
			}`
			page.MustSetViewport(390, 844, 1, false)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			read("42")
			// Covered on its own load and for a live arrival; crossing the
			// breakpoints with the panel open uncovers and covers it again.
			page.MustElement("#panel-control-overview").MustFocus()
			load("Enter on the control opens the sheet, focused, over an inert conversation", func() { page.Keyboard.MustType(input.Enter) }, opened)
			arrive("100")
			read("42")
			for _, size := range [][2]int{{390, 844}, {320, 568}} {
				page.MustSetViewport(size[0], size[1], 1, false)
				if problem := page.MustEval(sheet).Str(); problem != "" {
					t.Fatalf("%d px: %s", size[0], problem)
				}
			}
			page.MustSetViewport(1280, 800, 1, false)
			read("42,42,100")
			if page.MustEval(`() => document.getElementById('conversation').inert || document.getElementById('panel-backdrop').checkVisibility()`).Bool() {
				t.Fatal("the beside panel left the conversation covered")
			}
			page.MustSetViewport(1000, 800, 1, false)
			arrive("101")
			read("42,42,100")
			if !page.MustEval(`() => getComputedStyle(document.getElementById('side-panel')).position === 'absolute' && document.getElementById('conversation').inert`).Bool() {
				t.Fatal("the middle range does not overlay the conversation")
			}
			page.MustSetViewport(390, 844, 1, false)
			// Tab and Shift-Tab stay in the sheet.
			page.MustElement("#panel-heading").MustFocus()
			for i := range 16 {
				if i < 8 {
					page.Keyboard.MustType(input.Tab)
				} else {
					page.KeyActions().Press(input.ShiftLeft).Type(input.Tab).MustDo()
				}
				if !page.MustEval(`() => document.getElementById('side-panel').contains(document.activeElement)`).Bool() {
					t.Fatalf("Tab %d left the sheet for %s", i+1, page.MustEval(`() => document.activeElement.outerHTML.slice(0, 80)`).Str())
				}
			}
			// The wheel over the dimmed conversation scrolls nothing behind it.
			const scrolled = `() => { const l = document.getElementById('message-list'); return [l.scrollHeight > l.clientHeight && l.scrollTop > 0, l.scrollTop, document.scrollingElement.scrollTop].join(); }`
			before := page.MustEval(scrolled).Str()
			page.Mouse.MustMoveTo(195, 40).MustScroll(0, -600)
			page.Mouse.MustScroll(0, 600)
			time.Sleep(200 * time.Millisecond)
			if after := page.MustEval(scrolled).Str(); !strings.HasPrefix(before, "true,") || after != before {
				t.Fatalf("the conversation behind the sheet scrolled: %s to %s", before, after)
			}
			// A tall sheet stops at 80 % and scrolls itself.
			page.MustElement("#side-panel summary").MustClick()
			if !page.MustEval(`() => { const p = document.getElementById('side-panel'), h = document.documentElement.clientHeight;
				return Math.abs(p.getBoundingClientRect().height - 0.8 * h) <= 1 && p.scrollHeight > p.clientHeight; }`).Bool() {
				t.Fatal("a tall sheet is not 80 % high with its own scrolling")
			}
			// Mark as read from the sheet sends the newest shown cursor;
			// closing reads what arrived since, now in the snapshot.
			page.MustElement(`#side-panel button[form="read-form"]`).MustClick()
			read("42,42,100,101")
			arrive("102")
			read("42,42,100,101")
			visit.cursor.Store(102)
			page.MustElement("#panel-heading").MustFocus()
			load("Escape closes the sheet and returns focus", func() { page.Keyboard.MustType(input.Escape) }, closed)
			read("42,42,100,101,102")
			page.MustElement("#panel-control-overview").MustFocus()
			load("Enter on the control opens the sheet, focused, over an inert conversation", func() { page.Keyboard.MustType(input.Enter) }, opened)
			load("the dimmed link closes the sheet and returns focus", func() { page.Mouse.MustMoveTo(195, 40).MustClick(proto.InputMouseButtonLeft) }, closed)
			if got := page.MustInfo().URL; got != server.URL+path {
				t.Fatalf("the dimmed link closed to %s", got)
			}
			read("42,42,100,101,102,102")

			// Without JavaScript the sheet is a page: its member paging, close
			// link and Mark as read work, and nothing reads automatically.
			plain := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer plain.MustClose()
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(plain); err != nil {
				t.Fatal(err)
			}
			plain.MustSetViewport(390, 844, 1, false)
			// rod's element clicks do not return with scripts disabled.
			follow := func(selector, want string) {
				var at struct{ X, Y float64 }
				if err := plain.MustEval(`s => { const r = document.querySelector(s).getBoundingClientRect(); return {x: r.x + r.width / 2, y: r.y + r.height / 2}; }`, selector).Unmarshal(&at); err != nil {
					t.Fatal(err)
				}
				plain.Mouse.MustMoveTo(at.X, at.Y).MustClick(proto.InputMouseButtonLeft)
				plain.MustWait(`want => location.pathname + location.search === want && document.readyState === 'complete'`, want)
			}
			plain.MustNavigate(server.URL + path + "/members").MustWaitLoad()
			if problem := plain.MustEval(sheet).Str(); problem != "" {
				t.Fatalf("without JavaScript: %s", problem)
			}
			follow(`#members-list ~ a`, path+"/members?after=07000000-0000-0000-0000-000000000000")
			follow("#panel-close", path)
			plain.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
			follow(`#side-panel button[form="read-form"]`, path)
			read("42,42,100,101,102,102,102 plain")
		})
	}
}
