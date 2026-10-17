package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestSendRetryBrowser loses the answer of a committed send against real
// PostgreSQL and the real stream, then sends again: the stored message is
// never posted twice, and text typed after it survives (#1179).
func TestSendRetryBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "send retry checks")
	const (
		recovered = "Your earlier message was sent. What you typed after it is kept."
		repeat    = "A different message was already sent from this composer. Your text is kept: send again to post it."
	)
	for _, tc := range []struct {
		name string
		// typed and retyped run on the textarea while the lost send and the
		// retry are in flight; retried, alert and invalid describe the
		// composer after the retry's answer.
		typed, retyped, retried, alert string
		invalid                        bool
		stored                         []string
	}{
		{"unchanged", "", "", "", "", false, []string{"first"}},
		{"added", "body.setRangeText('second', 5, 5, 'end')", "", "second", recovered, false, []string{"first", "second"}},
		{"edited", "body.value = 'First draft'", "", "First draft", "", true, []string{"first", "First draft"}},
		{"edited-in-retry", "body.setRangeText('second', 5, 5, 'end')", "body.value = 'First second'", "First second", repeat, false, []string{"first", "First second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			pool, scope := feedFixture(t)
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
			feedRequire(t, err)
			var account, topic kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT m.account_id,c.default_topic_id FROM member m JOIN channel c ON c.organization_id=m.organization_id WHERE m.organization_id=$1 AND m.id=$2 AND c.id=$3", scope.OrganizationID, scope.MemberID, scope.ChannelID).Scan(&account, &topic))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			path := view.ConversationURL("feed", scope.ChannelID, &topic)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "send retry")
			defer closePage()
			page.MustEvalOnNewDocument(`(() => { window.composerAnswers = 0;
 document.addEventListener('htmx:afterRequest', e => { if (e.target.id === 'message-composer') window.composerAnswers++; });
})()`)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			// Each send is held while the test types; only the first is
			// answered by the handler, which commits it, and then lost.
			var answered atomic.Bool
			inFlight, release := make(chan struct{}), make(chan struct{})
			router := page.HijackRequests()
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				lose := !answered.Swap(true)
				if lose {
					if err := h.LoadResponse(http.DefaultClient, true); err != nil {
						t.Error(err)
					}
				}
				select {
				case inFlight <- struct{}{}:
					<-release
				case <-ctx.Done():
				}
				if lose {
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				} else {
					h.ContinueRequest(&proto.FetchContinueRequest{})
				}
			})
			go router.Run()
			defer func() { _ = router.Stop() }()
			send := func(typed string) {
				t.Helper()
				n := page.MustEval(`() => window.composerAnswers`).Int()
				page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
				<-inFlight
				if typed != "" {
					page.MustEval(`() => { const body = document.getElementById('message-body'); ` + typed + `; }`)
				}
				release <- struct{}{}
				page.MustWait(`n => window.composerAnswers > n`, n)
			}
			// The composer: text, key, mark, alert, caret, aria-invalid, and
			// whether the help shows an error.
			state := func() []string {
				var got []string
				for _, v := range page.MustEval(`() => { const form = document.getElementById('message-composer'), body = form.elements.body;
 return [body.value, form.elements.idempotency_key.value, form.dataset.unconfirmedKey ?? '', document.getElementById('message-send-alert').textContent,
  body.selectionStart, body.getAttribute('aria-invalid'), document.getElementById('message-help').getAttribute('role') === 'alert'].map(String); }`).Arr() {
					got = append(got, v.Str())
				}
				return got
			}
			page.MustEval(`() => { document.getElementById('message-body').value = 'first'; }`)
			key := state()[1]
			// The fixture's five messages come first.
			stored := func(want []string) {
				t.Helper()
				rows, err := pool.Query(ctx, "SELECT body FROM message WHERE organization_id=$1 AND channel_id=$2 ORDER BY event_seq OFFSET 5", scope.OrganizationID, scope.ChannelID)
				feedRequire(t, err)
				got, err := pgx.CollectRows(rows, pgx.RowTo[string])
				feedRequire(t, err)
				if !slices.Equal(got, want) {
					t.Fatalf("stored %q, want %q", got, want)
				}
			}
			send(tc.typed)
			stored([]string{"first"})
			if state()[2] != key {
				t.Fatal("the lost answer did not mark the send unconfirmed")
			}
			send(tc.retyped)
			// The caret stays at the end, and the help shows the 409's error
			// exactly when the textarea is marked invalid.
			got := state()
			want := []string{tc.retried, got[1], "", tc.alert, fmt.Sprint(len(tc.retried)), fmt.Sprint(tc.invalid), fmt.Sprint(tc.invalid)}
			if !slices.Equal(got, want) || got[1] == key {
				t.Fatalf("after the retry: %v", got)
			}
			if tc.retried != "" {
				send("")
			}
			stored(tc.stored)
			page.MustWait(`want => { const shown = [...document.querySelectorAll('#message-items [id^="body-"]')].map(e => e.textContent.trim()).filter(text => text !== 'message');
 return JSON.stringify(shown) === JSON.stringify(want); }`, tc.stored)
		})
	}
}
