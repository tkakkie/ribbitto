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

func (v *sheetVisit) Overview(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64, _ int64) (conversation.ChannelPage, error) {
	return v.Page(ctx, m, channel, topic, before)
}

func (v *sheetVisit) Page(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := v.fakeMessages.Page(ctx, m, channel, topic, before)
	cursor := v.cursor.Load()
	page.FeedFirstUnread, page.TopicFirstUnread, page.EventCursor = 7, 8, &cursor
	return page, err
}

// noReadWindow is how long a check that expects no reading POST watches for
// one. read-visibility-v6.js reads with no delay of its own: from read-applied
// in the task that applies a stream event, from resize at the next frame. A
// stray POST after a resize arrived within 10 ms when measured (#1122); the
// rest is margin for a loaded machine. No such check follows a refused read,
// whose retry timer would need longer.
const noReadWindow = 200 * time.Millisecond

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
			// 90 s, as the read-visibility and welcome tests: each step below has
			// its own bound and names itself when it stalls (#1060).
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			steps := newStepRunner(t, ctx, 20*time.Second)
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
			// arrived wakes read once a reading POST has been recorded.
			arrived := make(chan struct{}, 1)
			var throttle atomic.Bool // refuse the next reading POST with 429
			var fail atomic.Bool     // fail the next reading POST with 500
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
					refuse, failed := throttle.Swap(false), fail.Swap(false)
					if refuse {
						cursor += " 429"
					}
					if failed {
						cursor += " 500"
					}
					mu.Lock()
					reads = append(reads, cursor)
					mu.Unlock()
					select {
					case arrived <- struct{}{}:
					default:
					}
					if refuse {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if failed {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				handler.ServeHTTP(w, r)
			}))
			// Teardown is bounded whatever happened before: each tab closes
			// under its own fresh 10 s context (boundedPage), and closing the
			// client connections first ends the event stream even if a tab
			// did not close, so server.Close cannot wait on it (#1119).
			defer server.Close()
			defer server.CloseClientConnections()
			path := view.ChannelURL("acme", kernel.ID{1})
			if topic {
				path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
			}
			// Each step runs under its own bound and names itself when it
			// fails, so a stall says where it happened instead of exhausting
			// ctx in a callback that cannot name it (#1060).
			var base, page, plainBase, plain *rod.Page
			step := func(what string) {
				stepCtx := steps.next(what)
				if base != nil {
					page = base.Context(stepCtx)
				}
				if plainBase != nil {
					plain = plainBase.Context(stepCtx)
				}
			}
			// read checks that exactly the expected reading POSTs have arrived,
			// returning once they have; within 2 s, which covers a 429's first
			// retry (at most 1 s with Retry-After 0, read-visibility-v6.js).
			// Expecting none, it watches for one for noReadWindow instead. The
			// list is cumulative: a POST arriving after a check returned is still
			// compared by the next one.
			var want []string
			read := func(more ...string) {
				t.Helper()
				want = append(want, more...)
				need, bound := len(want), 2*time.Second
				if len(more) == 0 {
					need, bound = len(want)+1, noReadWindow
				}
				timer := time.NewTimer(bound)
				defer timer.Stop()
				recorded := func() (int, string) {
					mu.Lock()
					defer mu.Unlock()
					return len(reads), strings.Join(reads, ",")
				}
				n, got := recorded()
				for n < need {
					select {
					case <-arrived:
					case <-timer.C:
						need = 0 // one last look, then stop
					}
					n, got = recorded()
				}
				if got != strings.Join(want, ",") {
					t.Fatalf("%s: reading POSTs %q, want %q (at %s)", steps.current, got, want, steps.since())
				}
			}
			tab, closeTab := boundedPage(t, browser, ctx, "panel sheet")
			defer closeTab()
			base = tab.WithPanic(steps.fail)
			page = base
			wait := func(what, js string, args ...any) {
				t.Helper()
				if err := page.Timeout(5 * time.Second).Wait(rod.Eval(js, args...)); err != nil {
					t.Fatalf("%s: %v (at %s)", what, err, steps.since())
				}
			}
			send := func(out realtime.Outgoing) {
				t.Helper()
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				select {
				case frames <- out:
				case <-timer.C:
					t.Fatalf("%s: no stream took frame %d within 10 s (at %s)", steps.current, out.ID, steps.since())
				case <-page.GetContext().Done():
					t.Fatalf("%s: the step ran out before a stream took frame %d (at %s)", steps.current, out.ID, steps.since())
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
				// The function WaitNavigation returns ignores its context's end, so
				// that end is checked: a missed load fails by name instead of
				// costing 10 s silently.
				loadCtx, stop := context.WithTimeout(page.GetContext(), 10*time.Second)
				defer stop()
				navigated := page.Context(loadCtx).WaitNavigation(proto.PageLifecycleEventNameLoad)
				action()
				navigated()
				if page.GetContext().Err() != nil {
					t.Fatalf("%s: %s: the step's 20 s ran out before the load event (at %s)", steps.current, what, steps.since())
				}
				if loadCtx.Err() != nil {
					t.Fatalf("%s: no load event within 10 s (at %s)", what, steps.since())
				}
				wait(what, want)
			}
			open := func() {
				t.Helper()
				page.MustElement("#panel-control-overview").MustFocus()
				load("Enter on the control opens the dialog with focus inside", func() { steps.device(page, "Enter", func(p *rod.Page) { p.Keyboard.MustType(input.Enter) }) },
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
					t.Fatalf("%s: %s (at %s)", where, problem, steps.since())
				}
				role("dialog modal")
			}
			trap := func(where string) {
				t.Helper()
				page.MustElement("#panel-heading").MustFocus()
				for i := range 16 {
					steps.device(page, fmt.Sprintf("Tab %d", i+1), func(p *rod.Page) {
						if i < 8 {
							p.Keyboard.MustType(input.Tab)
						} else {
							p.KeyActions().Press(input.ShiftLeft).Type(input.Tab).MustDo()
						}
					})
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
				step(fmt.Sprintf("%d px: Mark as read in a loaded modal", width))
				size(width, 800)
				page.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
				wait(fmt.Sprintf("%d px: the overview opens as a modal", width), `() => document.getElementById('side-panel').getAttribute('aria-modal') === 'true'`)
				seq := 50 + 10*i
				live(seq)
				read()
				steps.device(page, "Mark as read", func(p *rod.Page) { p.MustElement(`#side-panel button[form="read-form"]`).MustClick() })
				read(fmt.Sprint(seq))
				live(seq + 1)
				throttle.Store(true)
				steps.device(page, "Mark as read", func(p *rod.Page) { p.MustElement(`#side-panel button[form="read-form"]`).MustClick() })
				live(seq + 2)
				read(fmt.Sprint(seq+1, " 429"), fmt.Sprint(seq+1))
			}
			step("390 px: the conversation reads its snapshot")
			size(390, 844)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			read("42")
			step("390 px: modal on opening, with the stream applied behind it")
			// Modal on its own load; the stream still updates the page behind,
			// and nothing it shows is read.
			open()
			modal("390 px on opening")
			live(100)
			send(realtime.Outgoing{Ephemeral: true, Name: "typing", Data: streamTestMarkup(t, view.TypingIndicator(typing.Snapshot{Typists: []typing.Typist{{Handle: "alice"}}}))})
			wait("typing shows behind the dialog", `() => document.getElementById('typing-status').textContent.includes('alice')`)
			read()
			step("sheet geometry at 390 and 320 px")
			// The sheet slides in on opening; arrival-v1.js drops the mark at its end.
			wait("the panel's arrival motion ends", `() => !('arrivePanel' in document.documentElement.dataset)`)
			for _, width := range [][2]int{{390, 844}, {320, 568}} {
				size(width[0], width[1])
				if problem := page.MustEval(sheet).Str(); problem != "" {
					t.Fatalf("%d px: %s", width[0], problem)
				}
			}
			// Sheet and overlay are one modal: crossing desk either way keeps it.
			step("crossing desk with the panel open")
			size(1000, 800)
			if page.MustEval(`() => getComputedStyle(document.getElementById('side-panel')).position`).Str() != "absolute" {
				t.Fatal("1000 px: the panel does not overlay the conversation")
			}
			modal("390 → 1000 px")
			trap("1000 px")
			// Below wide Mark as read lives in the overview only, as on a phone.
			if page.MustEval(`() => document.getElementById('read-form').checkVisibility()`).Bool() {
				t.Fatal("1000 px: the header shows Mark as read")
			}
			size(390, 844)
			modal("1000 → 390 px")
			size(1000, 800)
			read()
			// From wide the panel is beside the conversation again: released,
			// and reading resumes (the snapshot, then the arrival).
			step("1280 px releases the modal")
			size(1280, 800)
			read("42", "100")
			if !page.MustEval(`() => { const p = document.getElementById('side-panel');
				return !p.hasAttribute('role') && !p.hasAttribute('aria-modal') && !document.querySelector('[inert]') && !document.getElementById('panel-backdrop').checkVisibility(); }`).Bool() {
				t.Fatal("1280 px: the panel is still modal")
			}
			role("complementary")
			if !page.MustEval(`() => !!document.querySelector('#read-form button')?.checkVisibility()`).Bool() {
				t.Fatal("1280 px: the header lacks Mark as read")
			}
			step("1280 → 1000 px: modal again, manual reading")
			size(1000, 800)
			modal("1280 → 1000 px")
			live(101)
			read()
			// Mark as read in the dialog sends the newest shown cursor.
			steps.device(page, "Mark as read", func(p *rod.Page) { p.MustElement(`#side-panel button[form="read-form"]`).MustClick() })
			read("101")
			step("390 px: trap and manual reading")
			size(390, 844)
			trap("390 px")
			live(102)
			read()
			steps.device(page, "Mark as read", func(p *rod.Page) { p.MustElement(`#side-panel button[form="read-form"]`).MustClick() })
			read("102")
			// The wheel over the dimmed page scrolls nothing behind it.
			step("390 px: the page behind does not scroll; a tall sheet")
			const scrolled = `() => { const l = document.getElementById('message-list'); return [l.scrollHeight > l.clientHeight && l.scrollTop > 0, l.scrollTop, document.scrollingElement.scrollTop].join(); }`
			before := page.MustEval(scrolled).Str()
			steps.device(page, "the wheel over the dimmed page", func(p *rod.Page) {
				p.Mouse.MustMoveTo(195, 40).MustScroll(0, -600)
				p.Mouse.MustScroll(0, 600)
			})
			time.Sleep(200 * time.Millisecond)
			if after := page.MustEval(scrolled).Str(); !strings.HasPrefix(before, "true,") || after != before {
				t.Fatalf("the conversation behind the sheet scrolled: %s to %s", before, after)
			}
			// A tall sheet stops at 80 % and scrolls itself.
			steps.device(page, "opening the image picker", func(p *rod.Page) { p.MustElement("#side-panel summary").MustClick() })
			if !page.MustEval(`() => { const p = document.getElementById('side-panel'), h = document.documentElement.clientHeight;
				return Math.abs(p.getBoundingClientRect().height - 0.8 * h) <= 1 && p.scrollHeight > p.clientHeight; }`).Bool() {
				t.Fatal("a tall sheet is not 80 % high with its own scrolling")
			}
			// Escape, the close link and the dimmed page each close it at both
			// widths, returning focus; the closed page reads its snapshot.
			visit.cursor.Store(102)
			step("Escape at 390 px")
			page.MustElement("#panel-heading").MustFocus()
			closed("Escape at 390 px", func() { steps.device(page, "Escape", func(p *rod.Page) { p.Keyboard.MustType(input.Escape) }) })
			read("102")
			step("Escape at 1000 px")
			size(1000, 800)
			open()
			closed("Escape at 1000 px", func() { steps.device(page, "Escape", func(p *rod.Page) { p.Keyboard.MustType(input.Escape) }) })
			read("102")
			step("the close link at 1000 px")
			open()
			closed("the close link at 1000 px", func() {
				steps.device(page, "the close link", func(p *rod.Page) { p.MustElement("#panel-close").MustClick() })
			})
			read("102")
			step("the dimmed sidebar at 1000 px")
			open()
			closed("the dimmed sidebar at 1000 px", func() {
				steps.device(page, "a click on the dimmed sidebar", func(p *rod.Page) { p.Mouse.MustMoveTo(150, 400).MustClick(proto.InputMouseButtonLeft) })
			})
			read("102")
			step("the dimmed conversation at 390 px")
			size(390, 844)
			open()
			closed("the dimmed conversation at 390 px", func() {
				steps.device(page, "a click on the dimmed conversation", func(p *rod.Page) { p.Mouse.MustMoveTo(195, 40).MustClick(proto.InputMouseButtonLeft) })
			})
			read("102")
			step("1280 px: the panel beside the conversation")
			size(1280, 800)
			// Installed before the page's scripts, so its resize listener runs
			// first: at the target, listeners run in the order they were added.
			page.MustEvalOnNewDocument(`(() => {
				const test = window.panelTest = {hidden: false, held: false};
				Object.defineProperty(document, 'visibilityState', {configurable: true, get: () => test.hidden ? 'hidden' : 'visible'});
				Object.defineProperty(document, 'hidden', {configurable: true, get: () => test.hidden});
				addEventListener('resize', e => { if (test.held) e.stopImmediatePropagation(); });
			})()`)
			page.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
			read("102")
			// A failed read waits for a newer trigger; narrowing must not retry it
			// behind the dialog, and widening again does.
			step("1280 → 1000 px with a failed read pending")
			fail.Store(true)
			live(103)
			read("103 500")
			size(1000, 800)
			modal("1280 → 1000 px with a failed read")
			read()
			size(1280, 800)
			read("103")
			// A page shown again after its size crossed wide while hidden gets
			// its resize event only at its next rendering update, yet reading on
			// showing follows the size it is shown at. A headless tab is never
			// hidden, so the test overrides the page's visibility, dispatches the
			// lifecycle events and holds resize events back until shown (#1125).
			const released = `() => { const p = document.getElementById('side-panel'); return !p.hasAttribute('aria-modal') && !document.querySelector('[inert]'); }`
			seq := 103
			for _, persisted := range []bool{false, true} {
				for _, width := range []int{1000, 1280} {
					how := map[bool]string{false: "visibilitychange", true: "a persisted pageshow"}[persisted]
					step(fmt.Sprintf("hidden, resized to %d px, shown by %s before its resize", width, how))
					page.MustEval(`persisted => {
						panelTest.hidden = panelTest.held = true;
						if (persisted) dispatchEvent(new PageTransitionEvent('pagehide', {persisted: true}));
						document.dispatchEvent(new Event('visibilitychange'));
					}`, persisted)
					size(width, 800)
					wait("the layout takes the new width", `width => innerWidth === width`, width)
					seq++
					// A persisted pagehide releases the stream (#1499), so work
					// can only arrive again after the pageshow reconnects it.
					if !persisted {
						live(seq) // unread work arrives while hidden
					}
					page.MustEval(`persisted => {
						panelTest.hidden = false;
						document.dispatchEvent(new Event('visibilitychange'));
						if (persisted) dispatchEvent(new PageTransitionEvent('pageshow', {persisted: true}));
					}`, persisted)
					if persisted {
						live(seq)
					}
					if width < 1200 {
						read()
					} else {
						read(fmt.Sprint(seq))
					}
					page.MustEval(`() => { panelTest.held = false; dispatchEvent(new Event('resize')); }`)
					if width < 1200 {
						modal(fmt.Sprintf("shown at %d px", width))
					} else {
						wait("the panel is released", released)
					}
				}
			}

			// Without JavaScript the panel is a page: its member paging, close
			// link and Mark as read work, and nothing reads automatically.
			plainTab, closePlainTab := boundedPage(t, browser, ctx, "panel sheet without JavaScript")
			defer closePlainTab()
			plainBase = plainTab.WithPanic(steps.fail)
			step("without JavaScript: the members page")
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(plain); err != nil {
				t.Fatal(err)
			}
			plain.MustSetViewport(390, 844, 1, false)
			// rod's element clicks can stall with scripts disabled; WaitLoad
			// also hangs if the load is still pending. The test's own
			// evaluations still run.
			navigate := func(url string) {
				if err := navigateLoaded(plain, url); err != nil {
					t.Fatalf("%s: %v (at %s)", steps.current, err, steps.since())
				}
			}
			follow := func(selector, want string) {
				var at struct{ X, Y float64 }
				if err := plain.MustEval(`s => { const r = document.querySelector(s).getBoundingClientRect(); return {x: r.x + r.width / 2, y: r.y + r.height / 2}; }`, selector).Unmarshal(&at); err != nil {
					t.Fatal(err)
				}
				steps.device(plain, "a click on "+selector, func(p *rod.Page) { p.Mouse.MustMoveTo(at.X, at.Y).MustClick(proto.InputMouseButtonLeft) })
				if err := plain.Timeout(10 * time.Second).Wait(rod.Eval(`want => location.pathname + location.search === want && document.readyState === 'complete'`, want)); err != nil {
					t.Fatalf("without JavaScript, %s → %s: %v (at %s)", selector, want, err, steps.since())
				}
			}
			navigate(server.URL + path + "/members")
			if problem := plain.MustEval(sheet).Str(); problem != "" {
				t.Fatalf("without JavaScript: %s", problem)
			}
			follow(`#members-list ~ a`, path+"/members?after=07000000-0000-0000-0000-000000000000")
			follow("#panel-close", path)
			for _, width := range []int{390, 1000} {
				step(fmt.Sprintf("without JavaScript: Mark as read at %d px", width))
				plain.MustSetViewport(width, 800, 1, false)
				navigate(server.URL + path + "/overview")
				follow(`#side-panel button[form="read-form"]`, path)
				read("102 plain")
			}
		})
	}
}
