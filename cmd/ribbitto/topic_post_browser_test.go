package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestTopicPostBrowser posts with JavaScript from a topic view against real
// PostgreSQL and the real stream: only the composer's state is replaced, and the own
// message arrives once through the topic's stream (#1154).
func TestTopicPostBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "topic posting checks")
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "topicpost", "general")
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "planning")
	handler, _, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
	acceptanceOK(t, err)
	topicURL := view.ConversationURL("topicpost", f.Channel.ID, &topic.ID)
	var mu sync.Mutex
	var composed []url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == topicURL {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			form := url.Values{"hx": {r.Header.Get("HX-Request")}}
			for key, values := range r.PostForm {
				form[key] = values
			}
			mu.Lock()
			composed = append(composed, form)
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	owner := sidebarBrowser(t, pool, server, f.AccountID)
	// Enough history that the list scrolls, so "scrolled into view" means something.
	// Seed through the use case to leave the HTTP post budget for browser sends.
	posting := conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
	membership := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	for i := range 30 {
		_, err := posting.PostToTopic(ctx, membership, f.Channel.ID, &topic.ID, fmt.Sprintf("earlier %02d\nsecond line\nthird line", i))
		acceptanceOK(t, err)
	}
	page, closePage := boundedPage(t, browser, ctx, "topic post")
	defer closePage()
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	for _, cookie := range owner.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: server.URL, Path: "/", Secure: true})
	}
	page.MustNavigate(server.URL + topicURL).MustWaitLoad()
	// A full page load would drop this marker.
	page.MustEval(`() => { window.topicPostMarker = 'kept'; }`)
	for _, size := range [][2]int{{1280, 800}, {390, 844}} {
		t.Run(fmt.Sprint(size[0]), func(t *testing.T) {
			page.MustSetViewport(size[0], size[1], 1, false)
			text := fmt.Sprintf("own topic post %d", size[0])
			// Away from the newest message, so the own post must scroll.
			if !page.MustEval(`() => {
				const pane = document.getElementById('message-list');
				pane.scrollTop = 0;
				return document.getElementById('message-items').lastElementChild.getBoundingClientRect().top > pane.getBoundingClientRect().bottom;
			}`).Bool() {
				t.Fatal("the newest message is still in view; the scroll check would prove nothing")
			}
			shown := page.MustEval(`() => document.querySelector('#organization-stream').dataset.eventCursor`).Str()
			inputStep(t, page, "typing the topic post", func(p *rod.Page) { p.MustElement("#message-body").MustInput(text) })
			inputStep(t, page, "sending with Enter", func(p *rod.Page) { p.Keyboard.MustType(input.Enter) })
			if err := page.Timeout(10 * time.Second).Wait(rod.Eval(`text => [...document.querySelectorAll('#message-items div[id^=body-]')].some(e => e.textContent === text) && document.getElementById('message-body').value === '' && document.activeElement.id === 'message-body'`, text)); err != nil {
				t.Fatalf("topic post did not show, clear and keep focus: %v", err)
			}
			var id kernel.ID
			var seq int64
			acceptanceOK(t, pool.QueryRow(ctx, "SELECT id, event_seq FROM message WHERE organization_id = $1 AND topic_id = $2 AND body = $3", f.OrganizationID, topic.ID, text).Scan(&id, &seq))
			domID := view.MessageDOMID(id)
			// The shown cursor advances once the stream applies the own message.
			if err := page.Timeout(5 * time.Second).Wait(rod.Eval(`seq => document.querySelector('#organization-stream').dataset.eventCursor === seq`, strconv.FormatInt(seq, 10))); err != nil {
				t.Fatalf("stream application did not advance the shown cursor to %d: %v", seq, err)
			}
			// Leave time for a duplicate delivery or a late navigation.
			page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 300))`)
			if problem := page.MustEval(`(id, text) => {
				if (window.topicPostMarker !== 'kept') return 'the page navigated';
				if (document.querySelectorAll('#' + id).length !== 1 || [...document.querySelectorAll('#message-items div[id^=body-]')].filter(e => e.textContent === text).length !== 1) return 'own message not shown exactly once';
				if (!document.getElementById('message-items').contains(document.getElementById(id))) return 'own message outside #message-items';
				const item = document.getElementById(id).getBoundingClientRect(), pane = document.getElementById('message-list').getBoundingClientRect();
				if (item.top < pane.top - 1 || item.bottom > pane.bottom + 1) return 'own message not scrolled into view';
				return '';
			}`, domID, text).Str(); problem != "" {
				t.Fatal(problem)
			}
			if got := page.MustInfo().URL; got != server.URL+topicURL {
				t.Fatalf("page moved to %s", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(composed) != 1 || composed[0].Get("hx") != "true" || composed[0].Get("body") != text || composed[0].Get("cursor") != shown {
				t.Fatalf("want one enhanced topic POST with the shown cursor %s, got %v", shown, composed)
			}
			composed = nil
		})
	}
}
