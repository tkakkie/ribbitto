package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestReadPostBrowser(t *testing.T) {
	unavailable := func(reason string) {
		t.Helper()
		if os.Getenv("RIBBITTO_REQUIRE_BROWSER") == "1" {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	bin, found := launcher.LookPath()
	if !found {
		unavailable("no installed browser; Claude runs read visibility locally")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	control, err := launch.Launch()
	if err != nil {
		unavailable(err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(control).MustConnect()
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })
	for _, kind := range []string{"feed", "topic", "older-feed", "older-topic"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool, scope := feedFixture(t)
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{})
			feedRequire(t, err)
			var account, topicID kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT m.account_id,c.default_topic_id FROM member m JOIN channel c ON c.organization_id=m.organization_id WHERE m.organization_id=$1 AND m.id=$2 AND c.id=$3", scope.OrganizationID, scope.MemberID, scope.ChannelID).Scan(&account, &topicID))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			path := view.ChannelURL("feed", scope.ChannelID)
			if strings.Contains(kind, "topic") {
				path = view.ConversationURL("feed", scope.ChannelID, &topicID)
			}
			older := strings.HasPrefix(kind, "older")
			if older {
				path += "?before=3"
			}
			var posts atomic.Int32
			compositions := make(chan url.Values, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Supply the real session without making this test about signing in.
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				if r.Method == "POST" {
					posts.Add(1)
					if !strings.HasSuffix(r.URL.Path, "/read") {
						if err := r.ParseForm(); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						compositions <- r.PostForm
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			// Control visibility before scripts run; keep real htmx and application listeners.
			page.MustEvalOnNewDocument(`window.testVisibility = 'hidden'; Object.defineProperty(document, 'visibilityState', {get: () => window.testVisibility});`)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
			if posts.Load() != 0 {
				t.Fatal("hidden load sent a POST")
			}
			feedRanges(t, pool, scope, []unread.Range{})
			page.MustEval(`() => { document.querySelector('#organization-stream').dataset.eventCursor = '99'; window.testVisibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); }`)
			if !older {
				page.MustWait(`() => !document.querySelector('#read-form').classList.contains('htmx-request')`)
				if posts.Load() != 1 {
					t.Fatal("visible load did not POST once")
				}
				feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 7}})
			}
			page.MustEval(`() => { document.dispatchEvent(new Event('visibilitychange')); return new Promise(resolve => setTimeout(resolve, 150)); }`)
			want := int32(1)
			if older {
				want = 0
				feedRanges(t, pool, scope, []unread.Range{})
			}
			if posts.Load() != want {
				t.Fatalf("read posts=%d, want %d", posts.Load(), want)
			}
			if older {
				page.MustElement("#message-body").MustInput("own history post")
				page.MustElement("#message-composer button[type=submit]").MustClick()
				select {
				case form := <-compositions:
					if _, present := form["cursor"]; present || form.Get("body") != "own history post" {
						t.Fatalf("history composer submitted %v", form)
					}
				case <-ctx.Done():
					t.Fatal("history composer did not submit")
				}
			}
		})
	}
}
