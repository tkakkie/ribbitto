package web

import (
	"context"
	"fmt"
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
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Below the short height the conversation scrolls as a whole (#1056), but
// the scripts still move only #message-list: the newest message on load, a
// live arrival followed at the bottom, the reading position kept across
// older pages. Automatic reading still posts. A post's message is scrolled
// into view, which there also scrolls the conversation up from the composer.
func testShortHeightBrowser(t *testing.T, browser *rod.Browser) {
	for _, size := range [][2]int{{320, 200}, {1280, 800}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			topic, cursor := kernel.ID{2}, int64(42)
			seqs := make([]int, 40)
			for i := range seqs {
				seqs[i] = i + 1
			}
			snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}},
				EventCursor: &cursor, Reading: true, Older: true, Messages: streamTestMessages(topic, seqs[20:]...)}
			events := make(chan realtime.Outgoing, 1)
			reads := make(chan struct{}, 8)
			mux := http.NewServeMux()
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/organizations/acme/events":
					w.Header().Set("Content-Type", "text/event-stream")
					sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
					for err := sender.Heartbeat(r.Context()); err == nil; {
						select {
						case out := <-events:
							err = sender.Send(r.Context(), out)
						case <-r.Context().Done():
							return
						}
					}
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/read"):
					// Never block: a burst of POSTs must not keep server.Close waiting.
					select {
					case reads <- struct{}{}:
					default:
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost:
					response := snapshot
					response.PostedMessageID = &kernel.ID{44}
					templ.Handler(view.MessageComposer(response)).ServeHTTP(w, r)
				case r.URL.Query().Get("before") != "":
					response := snapshot
					response.Before, response.EventCursor, response.Older, response.Messages = 21, nil, false, streamTestMessages(topic, seqs[:20]...)
					templ.Handler(view.ChannelScreen("/static/css/app.css", response)).ServeHTTP(w, r)
				default:
					templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
				}
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			// Each step runs under its own bound and names itself when it fails,
			// with the time since the start (#1060). Every input here is a script
			// the page evaluates, which the step's context bounds; nothing goes
			// through rod's Mouse or Keyboard.
			steps := newStepRunner(t, ctx, 15*time.Second)
			tab, closeTab := boundedPage(t, browser, ctx, "short-height")
			defer closeTab()
			base := tab.WithPanic(steps.fail)
			page := base
			step := func(what string) { page = base.Context(steps.next(what)) }
			wait := func(what, js string, args ...any) {
				t.Helper()
				if err := page.Timeout(10 * time.Second).Wait(rod.Eval(js, args...)); err != nil {
					t.Fatalf("%s: %s: %v (at %s)", steps.current, what, err, steps.since())
				}
			}
			send := func(out realtime.Outgoing) {
				t.Helper()
				select {
				case events <- out:
				case <-page.GetContext().Done():
					t.Fatalf("%s: no stream took frame %d (at %s)", steps.current, out.ID, steps.since())
				}
			}
			const state = `() => { const l = document.getElementById('message-list'), m = document.getElementById('conversation');
				return {gap: l.scrollHeight - l.scrollTop - l.clientHeight, fromBottom: l.scrollHeight - l.scrollTop, conversation: m.scrollTop, window: scrollY}; }`
			atNewest := `() => { const l = document.getElementById('message-list'); return l.scrollHeight - l.scrollTop - l.clientHeight <= 2; }`

			step("loading the conversation")
			page.MustSetViewport(size[0], size[1], 1, false)
			page.MustNavigate(server.URL).MustWaitLoad()
			select {
			case <-reads:
			case <-page.GetContext().Done():
				t.Fatalf("%s: automatic reading did not post (at %s)", steps.current, steps.since())
			}
			wait("the list opens at the newest message", atNewest)

			step("a live message")
			before := page.MustEval(state)
			send(realtime.Outgoing{ID: 43, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 43)[0]))})
			wait("it is appended and followed", `() => !!document.querySelector('#message-items > li[data-event-seq="43"]') && (`+atNewest+`)()`)
			if after := page.MustEval(state); after.Get("conversation").Num() != before.Get("conversation").Num() || after.Get("window").Num() != before.Get("window").Num() {
				t.Errorf("%s moved the page: before %s, after %s", steps.current, before.JSON("", ""), after.JSON("", ""))
			}

			step("history paging")
			page.MustEval(`() => { document.getElementById('message-list').scrollTop = 0; }`)
			before = page.MustEval(state)
			page.MustEval(`() => document.querySelector('#load-older a').click()`)
			wait("older messages load and settle", `() => document.querySelector('#load-older').dataset.oldestSeq === '0' &&
				!!document.querySelector('#message-items > li[data-event-seq="1"]') && !document.querySelector('.htmx-settling')`)
			if after := page.MustEval(state); after.Get("fromBottom").Num() != before.Get("fromBottom").Num() {
				t.Errorf("%s moved the reader: before %s, after %s", steps.current, before.JSON("", ""), after.JSON("", ""))
			}

			// Post from the composer as a reader there would, scrolled to it.
			step("a composer post")
			page.MustEval(`() => { const m = document.getElementById('conversation'); m.scrollTop = m.scrollHeight;
				document.getElementById('message-composer-state').dataset.sent = '';
				document.getElementById('message-body').value = 'hello'; document.getElementById('message-composer').requestSubmit(); }`)
			// The response replaces the state; the stream script then takes its mark.
			wait("the post response settles", `() => { const state = document.getElementById('message-composer-state');
				return state.dataset.sent === undefined && state.dataset.postedMessage === undefined && !document.querySelector('.htmx-settling'); }`)
			send(realtime.Outgoing{ID: 44, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 44)[0]))})
			// Four 22 px lines of it, as the width audit's rule: at 320 px the
			// message is about as tall as the 128 px list.
			wait("the posted message is scrolled into view", `() => { const item = document.querySelector('#message-items > li[data-event-seq="44"]');
				if (!item || !(`+atNewest+`)()) return false;
				const r = item.getBoundingClientRect(), l = document.getElementById('message-list').getBoundingClientRect();
				return Math.min(r.bottom, l.bottom, document.documentElement.clientHeight) - Math.max(r.top, l.top, 0) >= Math.min(r.height, 88) - 0.5; }`)
		})
	}
}

// boundedPage creates a tab within 15 s, failing by name, and returns it on
// ctx with a close that has its own 10 s, so neither creation nor cleanup
// can wait on the whole test's deadline (#1060). Close it before the test
// server: an open tab keeps its stream request alive.
func boundedPage(t *testing.T, browser *rod.Browser, ctx context.Context, what string) (*rod.Page, func()) {
	t.Helper()
	// The page keeps the context it was created with (rod's Mouse and
	// Keyboard use it), so it is created with ctx and the wait is bounded
	// here instead.
	type created struct {
		page *rod.Page
		err  error
	}
	done := make(chan created, 1)
	go func() {
		page, err := browser.Context(ctx).Page(proto.TargetCreateTarget{})
		done <- created{page, err}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var page *rod.Page
	select {
	case c := <-done:
		if c.err != nil {
			t.Fatalf("%s: creating the page: %v", what, c.err)
		}
		page = c.page
	case <-timer.C:
		t.Fatalf("%s: creating the page did not finish within 15 s", what)
	}
	return page, func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := page.Context(closeCtx).Close(); err != nil {
			t.Errorf("%s: closing the page: %v", what, err)
		}
	}
}
