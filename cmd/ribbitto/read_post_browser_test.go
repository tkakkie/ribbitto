package main

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
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
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	control, err := launch.Launch()
	if err != nil {
		unavailable(err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(control).MustConnect()
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })
	for _, kind := range []string{"feed", "topic", "older-feed", "older-topic", "retry-feed"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
			var posts, active atomic.Int32
			var hold atomic.Bool
			var failNext atomic.Bool
			reads := make(chan string, 10)
			release := make(chan struct{}, 10)
			frames := make(chan string, 10)
			compositions := make(chan url.Values, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Control delivery while retaining the real read endpoint and writers.
				if strings.HasSuffix(r.URL.Path, "/events") {
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
				// Supply the real session without making this test about signing in.
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				if r.Method == "POST" {
					posts.Add(1)
					if strings.HasSuffix(r.URL.Path, "/read") {
						if active.Add(1) != 1 {
							t.Error("overlapping reading POSTs")
						}
						defer active.Add(-1)
						if err := r.ParseForm(); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						reads <- r.PostForm.Get("cursor")
						if failNext.Swap(false) {
							http.Error(w, "reading failed", http.StatusInternalServerError)
							return
						}
						if hold.Load() {
							select {
							case <-release:
							case <-r.Context().Done():
								return
							}
						}
					}
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
			if !older {
				assertRead := func(want string) {
					t.Helper()
					select {
					case got := <-reads:
						if got != want {
							t.Fatalf("reading cursor=%s, want %s", got, want)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				assertRead("6")
				// The synthetic received-only cursor above was never an application.
				page.MustEval(`() => document.querySelector('#organization-stream').dataset.eventCursor = '6'`)
				posterID := orgtest.Member(t, pool, scope.OrganizationID, identitytest.Account(t, pool, "writer@example.org", "Writer"), org.RoleMember, "writer", 1)
				poster := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
				membership := org.Membership{Organization: org.Organization{ID: scope.OrganizationID}, Member: org.Member{ID: posterID}}
				other := conversationtest.Topic(t, pool, scope.OrganizationID, scope.ChannelID, "other")
				post := func(topic kernel.ID, deliver bool) {
					t.Helper()
					m, err := poster.PostToTopic(ctx, membership, scope.ChannelID, &topic, "incoming")
					feedRequire(t, err)
					if deliver {
						var payload bytes.Buffer
						feedRequire(t, view.LiveMessageItem(view.Message{ID: m.ID, TopicID: topic, EventSeq: m.EventSeq, Body: m.Body}).Render(ctx, &payload))
						frames <- fmt.Sprintf("event: message\nid: %d\ndata: %s\n\n", m.EventSeq, payload.String())
					}
				}
				if kind == "retry-feed" {
					page.MustEval(`() => document.querySelector('#read-form').addEventListener('htmx:afterRequest', e => {
						document.body.dataset.readStatus = String(e.detail.xhr.status);
					})`)
					failNext.Store(true)
					post(topicID, true)
					assertRead("7")
					page.MustWait(`() => document.body.dataset.readStatus === '500'`)
					// Allow completion callbacks to run before supplying the next trigger.
					page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
					if posts.Load() != 2 || len(reads) != 0 {
						t.Fatal("failed reading POST retried without another trigger")
					}
					feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 7}})
					page.MustEval(`() => {
						window.testVisibility = 'hidden';
						document.dispatchEvent(new Event('visibilitychange'));
						window.testVisibility = 'visible';
						document.dispatchEvent(new Event('visibilitychange'));
					}`)
					assertRead("7")
					page.MustWait(`() => document.body.dataset.readStatus === '204'`)
					feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 8}})
					if posts.Load() != 3 || len(reads) != 0 {
						t.Fatal("visibility did not retry the failed cursor exactly once")
					}
					return
				}
				// Stop between receiving the frame and applying its server-rendered HTML.
				page.MustEval(`() => {
					const swap = htmx.swap;
					htmx.swap = (...args) => {
						htmx.swap = swap;
						window.applyHeld = () => swap(...args);
					};
				}`)
				post(topicID, true) // 7 received, not applied or shown.
				page.MustWait(`() => !!window.applyHeld`)
				page.MustEval(`() => document.dispatchEvent(new Event('visibilitychange'))`)
				feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 7}})
				if posts.Load() != 1 {
					t.Fatal("received but unapplied message caused a read")
				}
				hold.Store(true)
				page.MustEval(`() => window.applyHeld()`)
				assertRead("7")
				page.MustEval(`() => { window.testVisibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange')); }`)
				post(topicID, true) // 8 and 9 apply while hidden.
				post(topicID, true)
				page.MustWait(`() => document.querySelector('#organization-stream').dataset.appliedCursor === '9'`)
				if got := page.MustEval(`() => document.querySelector('#organization-stream').dataset.eventCursor`).Str(); got != "7" {
					t.Fatalf("hidden application advanced shown cursor to %s", got)
				}
				release <- struct{}{}
				page.MustWait(`() => !document.querySelector('#read-form').classList.contains('htmx-request')`)
				feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 8}})
				if posts.Load() != 2 {
					t.Fatal("hidden applications caused reading POSTs")
				}
				page.MustEval(`() => { window.testVisibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); }`)
				assertRead("9")
				post(other.ID, kind == "feed") // 10 is unseen in the topic view.
				post(topicID, true)            // 11 must replace every queued older cursor.
				page.MustWait(`() => document.querySelector('#organization-stream').dataset.eventCursor === '11'`)
				page.MustEval(`() => document.querySelector('#read-form').requestSubmit()`)
				if posts.Load() != 3 {
					t.Fatal("a second read started while the first was in flight")
				}
				release <- struct{}{}
				assertRead("11")
				release <- struct{}{}
				page.MustWait(`() => !document.querySelector('#read-form').classList.contains('htmx-request')`)
				want := []unread.Range{{Lo: 0, Hi: 12}}
				if kind == "topic" {
					want = []unread.Range{{Lo: 0, Hi: 10}, {Lo: 11, Hi: 12}}
				}
				feedRanges(t, pool, scope, want)
				if posts.Load() != 4 || len(reads) != 0 {
					t.Fatal("reading did not coalesce to the newest shown cursor")
				}
				if kind == "feed" {
					page.MustEval(`() => document.addEventListener('htmx:afterSettle', e => {
						if (e.target.id === 'message-composer') document.body.dataset.ownResponse = 'done';
					})`)
					page.MustElement("#message-body").MustInput("own response without stream delivery")
					page.MustEval(`() => document.querySelector('#message-composer').requestSubmit()`)
					page.MustWait(`() => document.body.dataset.ownResponse === 'done'`)
					if form := <-compositions; form.Get("cursor") != "11" {
						t.Fatalf("own post cursor=%s, want 11", form.Get("cursor"))
					}
					page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 150))`)
					if posts.Load() != 5 || len(reads) != 0 || page.MustEval(`() => document.querySelector('#organization-stream').dataset.eventCursor`).Str() != "11" {
						t.Fatal("own response advanced the reading cursor")
					}
				}
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
