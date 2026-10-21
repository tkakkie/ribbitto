package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Control only the sidebar retry clock; HTTP replies and SSE probes gate delivery.
const sidebarUnreadBrowserClock = `(() => {
 const timers = new Map(); let next = 0;
 const original = window.setTimeout, cancel = window.clearTimeout;
 window.sidebarTest = {started: 0, delays: []};
 Math.random = () => 0.999999;
 window.setTimeout = (fn, ms, ...args) => {
  if (!/sidebar-unread-v3.js|message-stream-v14.js/.test(new Error().stack)) return original(fn, ms, ...args);
  window.sidebarTest.delays.push(ms);
  const id = --next; timers.set(id, () => fn(...args)); return id;
 };
 window.clearTimeout = id => { if (!timers.delete(id)) cancel(id); };
 window.sidebarTest.advance = () => { const pending = [...timers.values()]; timers.clear(); pending.forEach(fn => fn()); };
 window.sidebarTest.pending = () => timers.size;
 document.addEventListener('htmx:beforeSend', e => {
  if (e.detail.elt.id === 'sidebar-resync') window.sidebarTest.started++;
 });
})()`

func TestSidebarUnreadBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	browser := launchBrowser(t, ctx, "sidebar acceptance")
	t.Run("author's other tab", func(t *testing.T) {
		pool := pgtest.New(t)
		f := newReadSidebarFixture(t, pool)
		u, err := url.Parse(f.reader.server.URL)
		acceptanceOK(t, err)
		var pages []*rod.Page
		for _, path := range []string{f.topicA, f.feed + "?before=6"} {
			page, closePage := boundedPage(t, browser, ctx, "author tab")
			defer closePage()
			for _, cookie := range f.reader.client.Jar.Cookies(u) {
				page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: f.reader.server.URL, Path: "/", Secure: true})
			}
			page.MustEvalOnNewDocument(`Object.defineProperty(document,'visibilityState',{get:()=> 'hidden'});
				document.addEventListener('htmx:sseMessage',e=>{if(e.detail.type==='sidebar') document.body.dataset.ready='yes';});`)
			page.MustNavigate(f.reader.server.URL + path).MustWaitLoad()
			page.MustWait(`() => document.body.dataset.ready === 'yes'`)
			pages = append(pages, page)
		}
		response, _, err := f.reader.send(f.reader.request(t, "POST", f.topicA, url.Values{"body": {"own post reads A"}, "cursor": {"6"}}))
		acceptanceOK(t, err)
		if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != f.topicA {
			t.Fatalf("posting response = %+v; want redirect to %s", response, f.topicA)
		}
		f.reader.visit(t, "GET", response.Header.Get("Location"), nil, http.StatusOK)
		for _, page := range pages {
			page.MustWait(`id => document.getElementById('link-'+id).querySelector('[data-unread-count]').dataset.unreadCount === '3'`, view.ChannelDOMID(f.channel))
			if !page.MustEval(`id => document.getElementById('link-'+id).querySelector('[data-unread-count]').hidden`, view.TopicDOMID(f.a)).Bool() {
				t.Fatal("own post did not clear A in the author's other tab")
			}
		}
	})

	for _, kind := range []string{"older-feed", "older-topic", "list", "hidden", "DOM", "malformed", "sequence-string", "sequence-fraction", "sequence-unsafe", "failed-request", "rate-limited", "during-resync", "continuous-posts", "before-request", "loss-during-resync", "overflow", "older-loss-snapshot", "second-overflow", "reset-delay", "stream-release"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool := pgtest.New(t)
			f := newReadSidebarFixture(t, pool)
			olderPage := kind == "older-feed" || kind == "older-topic"
			path, selected := f.topicA, &f.a
			wantInterests := "sidebar,messages,typing"
			switch kind {
			case "older-feed":
				path, selected = f.feed+"?before=6", nil
				wantInterests = "sidebar"
			case "older-topic":
				path += "?before=6"
				wantInterests = "sidebar"
			case "list":
				path, selected = view.ChannelListURL("read", f.channel), nil
				wantInterests = "sidebar"
			}
			m := org.Membership{Organization: org.Organization{ID: f.organization, Slug: "read", Name: "read"}, Member: org.Member{ID: f.member, JoinedEventSeq: 1}}
			reader := conversationpg.NewReader(pool, nil, nil, compose.EventCursor, compose.PageCountsIn)
			snapshot, err := reader.Sidebar(ctx, m, f.channel, selected)
			acceptanceOK(t, err)
			u, err := url.Parse(f.reader.server.URL)
			acceptanceOK(t, err)
			cookies := f.reader.client.Jar.Cookies(u)
			frames := make(chan string)
			requests := make(chan chan int, 4)
			interests := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/events") {
					interests <- r.URL.Query().Get("want")
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, ": connected\n\n")
					_ = http.NewResponseController(w).Flush()
					for {
						select {
						case frame := <-frames:
							_, _ = fmt.Fprint(w, frame)
							_ = http.NewResponseController(w).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}
				if strings.HasSuffix(r.URL.Path, "/sidebar/resync") {
					reply := make(chan int, 1)
					select {
					case requests <- reply:
					case <-r.Context().Done():
						return
					}
					select {
					case status := <-reply:
						w.WriteHeader(status)
					case <-r.Context().Done():
					}
					return
				}
				// Cookies ignore ports: Chrome may carry the preceding fixture's
				// loopback session, which Session would resolve before an appended one.
				r.Header.Del("Cookie")
				for _, cookie := range cookies {
					r.AddCookie(cookie)
				}
				f.reader.server.Config.Handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "sidebar page")
			defer closePage()
			page.MustEvalOnNewDocument(sidebarUnreadBrowserClock + `;window.visible = 'hidden'; window.probe = 0; window.completed = 0;
    Object.defineProperty(document, 'visibilityState', {get: () => window.visible});
    document.addEventListener('htmx:sseOpen', e => { e.detail.source.addEventListener('probe', e => window.probe = +e.data); e.detail.source.addEventListener('reset', () => window.reset = true); });
    document.addEventListener('htmx:afterRequest', e => { if(e.target.id === 'sidebar-resync') window.completed++; });`)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			select {
			case want := <-interests:
				if want != wantInterests {
					t.Fatalf("interests = %s, want %s", want, wantInterests)
				}
			case <-ctx.Done():
				t.Fatal("no stream")
			}
			probe, completed := 0, 0
			send := func(frame string) {
				t.Helper()
				probe++
				select {
				case frames <- frame + fmt.Sprintf("event: probe\ndata: %d\n\n", probe):
				case <-ctx.Done():
					t.Fatal("frame gate")
				}
				page.MustWait(`n => window.probe === n`, probe)
			}
			post := func(seq int, author kernel.ID) string {
				return fmt.Sprintf("event: unread\ndata: {\"channel\":%q,\"topic\":%q,\"seq\":%d,\"author\":%q}\n\n", realtimeID(f.channel), realtimeID(f.a), seq, realtimeID(author))
			}
			count := func(want int) {
				t.Helper()
				got := page.MustEval(`id => +document.getElementById('link-'+id).querySelector('[data-unread-count]').dataset.unreadCount`, view.ChannelDOMID(f.channel)).Int()
				if got != want {
					t.Fatalf("badge count = %d, want %d", got, want)
				}
			}
			both := func(want int) {
				t.Helper()
				count(want)
				got := page.MustEval(`id => +document.getElementById('link-'+id).querySelector('[data-unread-count]').dataset.unreadCount`, view.TopicDOMID(f.a)).Int()
				if got != want {
					t.Fatalf("topic badge count = %d, want %d", got, want)
				}
			}
			exact := func(seq int64, n int64) {
				snapshot.SnapshotSeq = seq
				snapshot.ChannelCounts[f.channel], snapshot.TopicCounts[f.a] = n, n
				frame, err := web.RenderSidebar(ctx, m, snapshot)
				acceptanceOK(t, err)
				send("event: sidebar\ndata: " + string(frame.Data) + "\n\n")
			}
			next := func() chan int {
				t.Helper()
				if !page.MustEval(`() => window.sidebarTest.delays.every(ms => ms >= 0 && ms <= 8000)`).Bool() {
					t.Fatal("resync jitter exceeded 8s backoff bound")
				}
				page.MustEval(`() => window.sidebarTest.advance()`)
				select {
				case reply := <-requests:
					return reply
				case <-ctx.Done():
					t.Fatal("resync request never arrived")
					return nil
				}
			}
			finish := func(reply chan int, status int) {
				reply <- status
				completed++
				page.MustWait(`n => window.completed === n`, completed)
			}
			noRetry := func(want int) {
				t.Helper()
				page.MustEval(`() => window.sidebarTest.advance()`)
				if !page.MustEval(`n => window.sidebarTest.started === n && window.sidebarTest.pending() === 0`, want).Bool() {
					t.Fatal("unexpected resync retry")
				}
			}
			burst := func(first, last int) {
				var frames strings.Builder
				for seq := first; seq <= last; seq++ {
					frames.WriteString(post(seq, kernel.ID{99}))
				}
				send(frames.String())
			}
			if kind == "reset-delay" {
				select {
				case frames <- "event: reset\ndata: reset\n\n":
				case <-ctx.Done():
					t.Fatal("reset gate")
				}
				page.MustWait(`() => window.reset && window.sidebarTest.pending() === 1`)
				if !page.MustEval(`() => window.sidebarTest.delays.at(-1) > 4999 && window.sidebarTest.delays.at(-1) <= 5000`).Bool() {
					t.Fatal("reset reload jitter outside 0–5s")
				}
				return
			}
			if kind == "stream-release" {
				// The persisted pagehide releases the stream through a bubbling
				// htmx:beforeCleanupElement that the sidebar script also hears.
				hide := func() {
					page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pagehide', {persisted: true}))`)
				}
				show := func() {
					page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pageshow', {persisted: true}))`)
				}
				reopened := func() {
					t.Helper()
					select {
					case want := <-interests:
						if want != wantInterests {
							t.Fatalf("interests = %s, want %s", want, wantInterests)
						}
					case <-ctx.Done():
						t.Fatal("persisted restore did not reopen the stream")
					}
				}
				// A restore with no resync pending.
				hide()
				show()
				reopened()
				finish(next(), 204)
				exact(7, 0)
				// The user leaves again within the 0–2 s window of the restore's
				// resync (rapid switching): the next restore must schedule it again.
				hide()
				show()
				reopened()
				if !page.MustEval(`() => window.sidebarTest.pending() === 1`).Bool() {
					t.Fatal("a stream release left the sidebar resync permanently unscheduled")
				}
				hide()
				if !page.MustEval(`() => window.sidebarTest.pending() === 0`).Bool() {
					t.Fatal("the persisted pagehide did not clear the pending resync")
				}
				show()
				reopened()
				if !page.MustEval(`() => window.sidebarTest.pending() === 1`).Bool() {
					t.Fatal("no resync scheduled after a pagehide within the restore's resync window")
				}
				finish(next(), 204)
				exact(8, 0)
				noRetry(2)
				return
			}
			other := kernel.ID{99}
			if olderPage || kind == "list" {
				exact(6, 99)
				send(post(7, other))
				count(100)
				if !page.MustEval(`id => document.getElementById('link-'+id).querySelector('[data-unread-count] [aria-hidden]').textContent === '99+'`, view.ChannelDOMID(f.channel)).Bool() {
					t.Fatal("100 did not display 99+")
				}
				send(post(7, other) + post(6, other) + post(8, f.member) + fmt.Sprintf("event: unread\ndata: {\"channel\":%q,\"seq\":9}\n\n", realtimeID(f.channel)))
				count(100)
				exact(6, 1)
				count(2) // Retained 7 reapplies; own 8 is ineligible.
				exact(6, 0)
				count(1) // Same sequence, later read: delivery order wins.
				exact(8, 0)
				send(post(7, other))
				count(0)
				page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`)
				reply := next()
				send(post(10, other))
				finish(reply, 204)
				exact(8, 1)
				count(2) // Preserve an increment that crossed a normal resync.
				return
			}
			if kind == "hidden" {
				// Real rendered message markup on the latest topic; message frames never increment.
				var markup strings.Builder
				acceptanceOK(t, view.LiveMessageItem(view.Message{ID: kernel.ID{7}, EventSeq: 7, Body: "hidden", TopicID: f.a}).Render(ctx, &markup))
				send("event: message\nid: 7\ndata: " + markup.String() + "\n\n")
				count(5)
				page.MustEval(`() => {window.visible='visible';document.dispatchEvent(new Event('visibilitychange'));}`)
				finish(next(), 204)
				exact(7, 0)
				count(0)
				page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`)
				finish(next(), 204)
				exact(7, 0)
				return
			}
			if strings.HasPrefix(kind, "sequence-") {
				exact(6, 5)
				send(post(7, other))
				both(6)
				seq := map[string]string{
					"sequence-string":   `"9007199254740993"`,
					"sequence-fraction": `100.5`,
					"sequence-unsafe":   `9007199254740992`,
				}[kind]
				send(fmt.Sprintf("event: unread\ndata: {\"channel\":%q,\"topic\":%q,\"seq\":%s,\"author\":%q}\n\n", realtimeID(f.channel), realtimeID(f.a), seq, realtimeID(other)))
				both(6)
				reply := next()
				send(post(8, other))
				both(6)      // Malformed sequences must stop increments until recovery completes.
				exact(8, 10) // Cover the highest valid sequence, never the malformed value.
				both(10)
				finish(reply, 204)
				both(10)
				send(post(9, other))
				both(11)
				noRetry(1)
				return
			}
			// A malformed badge is a failed DOM application, rather than a missing row.
			switch kind {
			case "DOM":
				page.MustEval(`id => document.getElementById('link-'+id).querySelector('[data-unread-count]').dataset.unreadCount='bad'`, view.ChannelDOMID(f.channel))
				send(post(7, other))
			case "overflow", "older-loss-snapshot", "second-overflow":
				burst(7, 1007)
				count(1005)
			case "failed-request":
				page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`)
				if !page.MustEval(`() => window.sidebarTest.delays.at(-1) > 1999 && window.sidebarTest.delays.at(-1) <= 2000`).Bool() {
					t.Fatal("visibility/pageshow jitter outside 0–2s")
				}
				finish(next(), 500)
				for i := 0; i < 6; i++ {
					page.MustEval(`() => window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`)
					if !page.MustEval(`i => window.sidebarTest.pending() === 1 && window.sidebarTest.delays.at(-1) > Math.min(250 * 2 ** (i+1),8000) - 1 && window.sidebarTest.delays.at(-1) <= 8000`, i).Bool() {
						t.Fatal("retry did not merge triggers or respect jittered exponential cap")
					}
					finish(next(), 500)
				}
			default:
				send("event: unread\ndata: broken\n\n")
			}
			if kind == "before-request" {
				exact(7, 10) // No dispatched request: an unsolicited snapshot cannot clear recovery.
				send(post(8, other))
				count(10)
			}
			reply := next()
			if kind == "DOM" {
				page.MustEval(`id => document.getElementById('link-'+id).querySelector('[data-unread-count]').dataset.unreadCount='5'`, view.ChannelDOMID(f.channel))
			}
			if kind == "rate-limited" {
				exact(7, 10) // A snapshot before a refused answer cannot clear recovery.
				finish(reply, http.StatusTooManyRequests)
				send(post(8, other))
				both(10)
				if !page.MustEval(`() => window.sidebarTest.pending() === 1`).Bool() {
					t.Fatal("429 did not reserve one resync retry")
				}
				retry := next()
				exact(7, 10)
				send(post(9, other))
				both(10) // Recovery stays open through the covering snapshot until HTTP success.
				finish(retry, http.StatusNoContent)
				both(12) // Only the successful retry may reapply retained 8 and 9.
				send(post(10, other))
				both(13)
				noRetry(2)
				return
			}
			if kind == "older-loss-snapshot" {
				exact(50, 49) // An earlier read's snapshot arrives after dispatch, before its answer.
				finish(reply, 204)
				both(49) // Dropped 7..1006 are not covered; retained 1007 cannot reapply yet.
				send(post(1008, other))
				both(49) // Recovery must still stop increments after HTTP success.
				if !page.MustEval(`() => window.sidebarTest.pending() === 1`).Bool() {
					t.Fatal("older snapshot did not reserve one resync retry")
				}
				retry := next()
				exact(1006, 1005) // Equality with the highest dropped sequence is sufficient.
				both(1005)        // SSE alone cannot clear recovery before the retry succeeds.
				finish(retry, 204)
				both(1007) // Retained 1007 and 1008 reapply once above the covering answer.
				send(post(1009, other))
				both(1008)
				noRetry(2)
				return
			}
			if kind == "continuous-posts" {
				burst(8, 57)
				count(5) // Retention alone cannot end recovery before the answer.
				finish(reply, 204)
				exact(32, 30)
				both(55) // Only 33..57 reapply above the answered snapshot.
				for seq := 58; seq <= 157; seq++ {
					send(post(seq, other))
					both(seq - 2)
					noRetry(1) // Continued posting must not recreate the recount loop.
				}
				return
			}
			if kind != "overflow" && kind != "second-overflow" {
				send(post(8, other))
				if kind == "before-request" {
					count(10)
				} else {
					count(5)
				}
			}
			if kind == "during-resync" {
				send(post(9, f.member) + fmt.Sprintf("event: unread\ndata: {\"channel\":%q,\"topic\":%q,\"seq\":10}\n\n", realtimeID(f.channel), realtimeID(f.a)))
				count(5)
				exact(7, 10) // SSE may answer before the HTTP success is observed.
				send(post(11, other))
				count(10)
				finish(reply, 204)
				both(12) // Both eligible events apply; own and legacy events do not.
				send(post(12, other))
				both(13)
				exact(7, 10)
				both(13) // Reapply once, preserving own/legacy exclusion.
				send(post(8, other) + post(12, other))
				both(13)
				noRetry(1)
				return
			}
			switch kind {
			case "overflow", "second-overflow":
				burst(1008, 2008) // Lose more than 1,000 retained events after dispatch.
				count(1005)
			case "loss-during-resync":
				send("event: unread\ndata: broken\n\n")
			default:
				finish(reply, 204)
				exact(7, 10)
				both(11) // Retained 8 is not a loss, including after DOM/request failure.
				send(post(9, other))
				both(12)
				noRetry(completed)
				return
			}
			finish(reply, 204)
			exact(7, 10)
			send(post(3000, other))
			count(10) // A loss since dispatch keeps recovery open, even with retained posts.
			retry := next()
			if kind == "second-overflow" {
				burst(3001, 4001) // Lose events again during the reserved retry.
				finish(retry, 204)
				exact(5000, 20)
				send(post(5001, other))
				count(20) // Even a covering sequence cannot clear a new loss.
				retry = next()
			}
			send(post(6001, other))
			finish(retry, 204)
			exact(6000, 30)
			both(31) // A loss-free answer reapplies retained 6001 without another retry.
			send(post(6002, other))
			both(32)
			noRetry(completed)
		})
	}
}

func realtimeID(id kernel.ID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}
