package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
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
	for _, tc := range []struct {
		name string
		// edit runs on the textarea after the lost answer; retried and alert
		// are the textarea and the alert after the retry's answer.
		edit, retried, alert string
		stored               []string
	}{
		{"unchanged", "", "", "", []string{"first"}},
		{"added", "body.value += 'second'", "second", "Your earlier message was sent. What you typed after it is kept.", []string{"first", "second"}},
		{"edited", "body.value = 'First draft'", "First draft", "", []string{"first", "First draft"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			pool, scope := feedFixture(t)
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
			feedRequire(t, err)
			var account kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT account_id FROM member WHERE organization_id=$1 AND id=$2", scope.OrganizationID, scope.MemberID).Scan(&account))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			path := view.ChannelURL("feed", scope.ChannelID)
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
			// Only the first send's answer is lost, after the handler committed.
			lost := make(chan struct{}, 1)
			lost <- struct{}{}
			router := page.HijackRequests()
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				select {
				case <-lost:
				default:
					h.ContinueRequest(&proto.FetchContinueRequest{})
					return
				}
				if err := h.LoadResponse(http.DefaultClient, true); err != nil {
					t.Error(err)
				}
				h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
			})
			go router.Run()
			defer func() { _ = router.Stop() }()
			send := func() {
				t.Helper()
				n := page.MustEval(`() => window.composerAnswers`).Int()
				page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
				page.MustWait(`n => window.composerAnswers > n`, n)
			}
			state := func() (string, string, string) {
				got := page.MustEval(`() => { const form = document.getElementById('message-composer');
 return [form.elements.body.value, form.elements.idempotency_key.value, document.getElementById('message-send-alert').textContent]; }`).Arr()
				return got[0].Str(), got[1].Str(), got[2].Str()
			}
			page.MustEval(`() => { document.getElementById('message-body').value = 'first'; }`)
			_, key, _ := state()
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
			send()
			stored([]string{"first"})
			if page.MustEval(`() => document.getElementById('message-composer').dataset.unconfirmedKey`).Str() != key {
				t.Fatal("the lost answer did not mark the send unconfirmed")
			}
			if tc.edit != "" {
				page.MustEval(`() => { const body = document.getElementById('message-body'); ` + tc.edit + `; }`)
			}
			send()
			text, retryKey, alert := state()
			if text != tc.retried || retryKey == key || alert != tc.alert {
				t.Fatalf("after the retry: text %q, alert %q, key changed %t", text, alert, retryKey != key)
			}
			if tc.retried != "" {
				send()
			}
			stored(tc.stored)
			page.MustWait(`want => { const shown = [...document.querySelectorAll('#message-items [id^="body-"]')].map(e => e.textContent.trim()).filter(text => text !== 'message');
 return JSON.stringify(shown) === JSON.stringify(want); }`, tc.stored)
		})
	}
}
