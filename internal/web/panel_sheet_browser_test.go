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
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
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

// Below the wide breakpoint an open panel is a modal dialog over the dimmed
// page, an overlay or below desk a bottom sheet: the page behind is inert
// but still live, automatic reading waits for the dialog, Mark as read does
// not, and the dialog keeps focus and scrolling to itself (#972).
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
			var throttle atomic.Bool // refuse the next reading POST with 429
			frames := make(chan realtime.Outgoing)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/organizations/acme/events" {
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
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/read") && r.ParseForm() == nil {
					cursor := r.PostForm.Get("cursor")
					if r.Header.Get("HX-Request") == "" {
						cursor += " plain"
					}
					refuse := throttle.Swap(false)
					if refuse {
						cursor += " 429"
					}
					mu.Lock()
					reads = append(reads, cursor)
					mu.Unlock()
					if refuse {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			path := view.ChannelURL("acme", kernel.ID{1})
			if topic {
				path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
			}
			// read waits at least 200 ms for exactly the expected reading POSTs.
			var want []string
			read := func(more ...string) {
				t.Helper()
				want = append(want, more...)
				got := ""
				for i := 0; i < 100 && (i < 10 || got != strings.Join(want, ",")); i++ {
					time.Sleep(20 * time.Millisecond)
					mu.Lock()
					got = strings.Join(reads, ",")
					mu.Unlock()
				}
				if got != strings.Join(want, ",") {
					t.Fatalf("reading POSTs %q, want %q", got, want)
				}
			}
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			wait := func(what, js string, args ...any) {
				t.Helper()
				if err := page.Timeout(5 * time.Second).Wait(rod.Eval(js, args...)); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
			}
			send := func(out realtime.Outgoing) {
				t.Helper()
				select {
				case frames <- out:
				case <-ctx.Done():
					t.Fatal("no stream took the frame")
				}
			}
			// live delivers a message on the stream: the page applies and shows
			// it, which would read it if nothing covered the conversation.
			live := func(seq int) {
				t.Helper()
				send(realtime.Outgoing{ID: int64(seq), Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(kernel.ID{5}, seq)[0]))})
				wait(fmt.Sprintf("message %d is shown behind the dialog", seq), `seq => document.getElementById('organization-stream').dataset.appliedCursor === String(seq) &&
					!!document.querySelector('#message-items > li[data-event-seq="' + seq + '"]')`, seq)
			}
			load := func(what string, action func(), want string) {
				t.Helper()
				navigated := page.Timeout(10 * time.Second).WaitNavigation(proto.PageLifecycleEventNameLoad)
				action()
				navigated()
				wait(what, want)
			}
			open := func() {
				t.Helper()
				page.MustElement("#panel-control-overview").MustFocus()
				load("Enter on the control opens the dialog with focus inside", func() { page.Keyboard.MustType(input.Enter) },
					`() => document.activeElement.id === 'panel-heading' && document.getElementById('side-panel').getAttribute('aria-modal') === 'true'`)
			}
			closed := func(what string, action func()) {
				t.Helper()
				load(what, action, `() => !document.getElementById('side-panel') && document.activeElement.id === 'panel-control-overview' && !document.querySelector('[inert]')`)
				if got := page.MustInfo().URL; got != server.URL+path {
					t.Fatalf("%s: closed to %s", what, got)
				}
			}
			// role reads the panel's role and modality as assistive technology does.
			role := func(want string) {
				t.Helper()
				tree, err := proto.AccessibilityGetPartialAXTree{ObjectID: page.MustElement("#side-panel").Object.ObjectID}.Call(page)
				got := ""
				if err == nil && len(tree.Nodes) > 0 && tree.Nodes[0].Role != nil {
					got = tree.Nodes[0].Role.Value.Str()
					for _, property := range tree.Nodes[0].Properties {
						if property.Name == proto.AccessibilityAXPropertyNameModal && property.Value.Value.Bool() {
							got += " modal"
						}
					}
				}
				if got != want {
					t.Fatalf("accessibility tree: panel is %q, want %q (%v)", got, want, err)
				}
			}
			// modal checks, once a resize has applied, that the panel is a modal
			// dialog with focus inside and nothing behind it takes focus or a pointer.
			modal := func(where string) {
				t.Helper()
				problem := ""
				for range 50 {
					if problem = page.MustEval(`() => {
						const panel = document.getElementById('side-panel'), dim = document.getElementById('panel-backdrop');
						if (panel.getAttribute('role') !== 'dialog' || panel.getAttribute('aria-modal') !== 'true') return 'not a modal dialog';
						if (!panel.contains(document.activeElement)) return 'focus outside: ' + document.activeElement.outerHTML.slice(0, 60);
						for (const e of document.querySelectorAll('#organization-stream a[href], #organization-stream button, #message-body')) {
							if (panel.contains(e) || e === dim) continue;
							if (!e.closest('[inert]')) return 'not inert: ' + e.outerHTML.slice(0, 80);
							const r = e.getBoundingClientRect(), hit = r.width && document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
							if (hit && hit !== dim && !panel.contains(hit)) return 'reachable by pointer: ' + e.outerHTML.slice(0, 80);
						}
						return '';
					}`).Str(); problem == "" {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if problem != "" {
					t.Fatalf("%s: %s", where, problem)
				}
				role("dialog modal")
			}
			trap := func(where string) {
				t.Helper()
				page.MustElement("#panel-heading").MustFocus()
				for i := range 16 {
					if i < 8 {
						page.Keyboard.MustType(input.Tab)
					} else {
						page.KeyActions().Press(input.ShiftLeft).Type(input.Tab).MustDo()
					}
					if !page.MustEval(`() => document.getElementById('side-panel').contains(document.activeElement)`).Bool() {
						t.Fatalf("%s: Tab %d left the dialog for %s", where, i+1, page.MustEval(`() => document.activeElement.outerHTML.slice(0, 80)`).Str())
					}
				}
			}
			size := func(width, height int) {
				page.MustSetViewport(width, height, 1, false)
			}
			const sheet = `() => {
				const width = document.documentElement.clientWidth, height = document.documentElement.clientHeight;
				const panel = document.getElementById('side-panel'), box = panel.getBoundingClientRect(), style = getComputedStyle(panel);
				const dim = document.getElementById('panel-backdrop')?.getBoundingClientRect() ?? {};
				if (style.position !== 'fixed' || box.left !== 0 || box.right !== width || Math.abs(box.bottom - height) > 0.5) return 'sheet not along the bottom: ' + JSON.stringify(box);
				if (style.borderTopLeftRadius !== '12px' || style.borderTopRightRadius !== '12px' || style.borderBottomLeftRadius !== '0px') return 'sheet corners';
				if (box.height > 0.8 * height + 0.5) return 'sheet taller than 80 %: ' + box.height + ' of ' + height;
				if (dim.left !== 0 || dim.top !== 0 || dim.right < width || dim.bottom < height) return 'page not dimmed';
				if (document.documentElement.scrollWidth > width || panel.scrollWidth > panel.clientWidth) return 'sideways scroll';
				return '';
			}`
			// Loaded modal, before any widening has sent a read: one Mark as
			// read covers what arrived since the snapshot, and a refused one
			// retries past the dialog without reading later arrivals.
			for i, width := range []int{390, 1000} {
				size(width, 800)
				page.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
				wait(fmt.Sprintf("%d px: the overview opens as a modal", width), `() => document.getElementById('side-panel').getAttribute('aria-modal') === 'true'`)
				seq := 50 + 10*i
				live(seq)
				read()
				page.MustElement(`#side-panel button[form="read-form"]`).MustClick()
				read(fmt.Sprint(seq))
				live(seq + 1)
				throttle.Store(true)
				page.MustElement(`#side-panel button[form="read-form"]`).MustClick()
				live(seq + 2)
				read(fmt.Sprint(seq+1, " 429"), fmt.Sprint(seq+1))
			}
			size(390, 844)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			read("42")
			// Modal on its own load; the stream still updates the page behind,
			// and nothing it shows is read.
			open()
			modal("390 px on opening")
			live(100)
			send(realtime.Outgoing{Ephemeral: true, Name: "typing", Data: streamTestMarkup(t, view.TypingIndicator(typing.Snapshot{Typists: []typing.Typist{{Handle: "alice"}}}))})
			wait("typing shows behind the dialog", `() => document.getElementById('typing-status').textContent.includes('alice')`)
			read()
			for _, width := range [][2]int{{390, 844}, {320, 568}} {
				size(width[0], width[1])
				if problem := page.MustEval(sheet).Str(); problem != "" {
					t.Fatalf("%d px: %s", width[0], problem)
				}
			}
			// Sheet and overlay are one modal: crossing desk either way keeps it.
			size(1000, 800)
			if page.MustEval(`() => getComputedStyle(document.getElementById('side-panel')).position`).Str() != "absolute" {
				t.Fatal("1000 px: the panel does not overlay the conversation")
			}
			modal("390 → 1000 px")
			trap("1000 px")
			size(390, 844)
			modal("1000 → 390 px")
			size(1000, 800)
			read()
			// From wide the panel is beside the conversation again: released,
			// and reading resumes (the snapshot, then the arrival).
			size(1280, 800)
			read("42", "100")
			if !page.MustEval(`() => { const p = document.getElementById('side-panel');
				return !p.hasAttribute('role') && !p.hasAttribute('aria-modal') && !document.querySelector('[inert]') && !document.getElementById('panel-backdrop').checkVisibility(); }`).Bool() {
				t.Fatal("1280 px: the panel is still modal")
			}
			role("complementary")
			size(1000, 800)
			modal("1280 → 1000 px")
			live(101)
			read()
			// Mark as read in the dialog sends the newest shown cursor.
			page.MustElement(`#side-panel button[form="read-form"]`).MustClick()
			read("101")
			size(390, 844)
			trap("390 px")
			live(102)
			read()
			page.MustElement(`#side-panel button[form="read-form"]`).MustClick()
			read("102")
			// The wheel over the dimmed page scrolls nothing behind it.
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
			// Escape, the close link and the dimmed page each close it at both
			// widths, returning focus; the closed page reads its snapshot.
			visit.cursor.Store(102)
			page.MustElement("#panel-heading").MustFocus()
			closed("Escape at 390 px", func() { page.Keyboard.MustType(input.Escape) })
			read("102")
			size(1000, 800)
			open()
			closed("Escape at 1000 px", func() { page.Keyboard.MustType(input.Escape) })
			read("102")
			open()
			closed("the close link at 1000 px", func() { page.MustElement("#panel-close").MustClick() })
			read("102")
			open()
			closed("the dimmed sidebar at 1000 px", func() { page.Mouse.MustMoveTo(150, 400).MustClick(proto.InputMouseButtonLeft) })
			read("102")
			size(390, 844)
			open()
			closed("the dimmed conversation at 390 px", func() { page.Mouse.MustMoveTo(195, 40).MustClick(proto.InputMouseButtonLeft) })
			read("102")

			// Without JavaScript the panel is a page: its member paging, close
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
			for _, width := range []int{390, 1000} {
				plain.MustSetViewport(width, 800, 1, false)
				plain.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
				follow(`#side-panel button[form="read-form"]`, path)
				read("102 plain")
			}
		})
	}
}
