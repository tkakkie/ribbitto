package main

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func recountAcceptanceAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	acceptanceOK(t, err)
	addr := ln.Addr().String()
	acceptanceOK(t, ln.Close())
	return addr
}

func recountAcceptanceWait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("composition did not reach the expected state within 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecountCompositionAcceptance(t *testing.T) {
	for _, mode := range []string{"default", "on", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			saveMutexSampling(t)
			for _, key := range []string{
				"RIBBITTO_POOL_MAX_CONNS", "RIBBITTO_RECOUNT_WORKERS", "RIBBITTO_DB_MAX_CONNECTIONS",
				"RIBBITTO_DB_RESERVED_CONNECTIONS", "RIBBITTO_DB_EXTERNAL_CONNECTIONS",
				"RIBBITTO_DB_PROCESSES", "RIBBITTO_DB_NORMAL_HEADROOM",
			} {
				t.Setenv(key, "stray-invalid")
			}
			t.Setenv("RIBBITTO_ASYNC_RECOUNTS", mode)
			if mode == "default" {
				acceptanceOK(t, os.Unsetenv("RIBBITTO_ASYNC_RECOUNTS"))
			}
			addr, metricsAddr := recountAcceptanceAddr(t), recountAcceptanceAddr(t)
			for key, value := range map[string]string{
				"RIBBITTO_ADDR": addr, "RIBBITTO_DEV_METRICS_ADDR": metricsAddr,
				"RIBBITTO_MAX_STREAMS": "2", "RIBBITTO_DEV_ASSETS": "",
				"RIBBITTO_SIGNUP": "off", "RIBBITTO_SETUP_TOKEN": "",
				"RIBBITTO_TRUSTED_PROXIES": "", "RIBBITTO_EVENT_RETENTION": "168h",
			} {
				t.Setenv(key, value)
			}
			logs, err := os.CreateTemp(t.TempDir(), "startup")
			acceptanceOK(t, err)
			t.Cleanup(func() { acceptanceOK(t, logs.Close()) })
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })

			pool := pgtest.New(t)
			databaseURL, err := url.Parse(os.Getenv("RIBBITTO_TEST_DATABASE_URL"))
			acceptanceOK(t, err)
			databaseURL.Path = "/" + pool.Config().ConnConfig.Database
			query := databaseURL.Query()
			query.Set("pool_max_conns", "7")
			databaseURL.RawQuery = query.Encode()
			f := conversationtest.OrganizationWithOwner(t, pool, "recount", "general")
			topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "selected")
			account := identitytest.Account(t, pool, "reader@example.org", "reader")
			orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
			m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
			posting := conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
			_, err = posting.PostToTopic(t.Context(), m, f.Channel.ID, &topic.ID, "unread")
			acceptanceOK(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			owned := make(chan *serviceLifecycle, 1)
			poolClosed := make(chan struct{}, 1)
			go func() {
				done <- serveWithLifecycle(ctx, databaseURL.String(), func(ctx context.Context, hub *realtime.Hub, closePool func()) *serviceLifecycle {
					l := newServiceLifecycle(ctx, hub, func() {
						poolClosed <- struct{}{}
						closePool()
					})
					owned <- l
					return l
				})
			}()
			stop := sync.OnceFunc(func() {
				cancel()
				select {
				case err := <-done:
					acceptanceOK(t, err)
				case <-time.After(5 * time.Second):
					t.Fatal("serve did not join and shut down within 5 s")
				}
			})
			t.Cleanup(stop)
			select {
			case <-owned:
			case <-time.After(5 * time.Second):
				t.Fatal("serve did not acquire its lifecycle")
			}
			client := &http.Client{Timeout: 2 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			snapshot := func() (s metricsSnapshot) {
				response, err := client.Get("http://" + metricsAddr + "/metrics")
				acceptanceOK(t, err)
				defer func() { acceptanceOK(t, response.Body.Close()) }()
				acceptanceOK(t, json.NewDecoder(response.Body).Decode(&s))
				return s
			}
			recountAcceptanceWait(t, func() bool {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err != nil {
					return false
				}
				acceptanceOK(t, conn.Close())
				return true
			})
			s := snapshot()
			if s.Pool.MaxConns != 7 {
				t.Fatalf("startup changed URL pool maximum: %d, want 7", s.Pool.MaxConns)
			}

			jar, err := cookiejar.New(nil)
			acceptanceOK(t, err)
			b := acceptanceBrowser{server: &httptest.Server{URL: "http://" + addr}, client: &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
			t.Cleanup(b.client.CloseIdleConnections)
			token, _, err := identitypg.NewSessions(pool, time.Now, nil).Create(t.Context(), account)
			acceptanceOK(t, err)
			u, err := url.Parse(b.server.URL)
			acceptanceOK(t, err)
			// serve is behind TLS in deployments; this test seeds a loopback HTTP cookie.
			jar.SetCookies(u, []*http.Cookie{{Name: middleware.SessionCookie, Value: token, Path: "/"}})
			path := view.ConversationURL("recount", f.Channel.ID, &topic.ID)
			_, page := b.visit(t, "GET", path+"/members", nil, 200)
			_, raw, found := strings.Cut(page, `sse-connect="`)
			if !found {
				t.Fatal("members page has no stream endpoint")
			}
			raw, _, _ = strings.Cut(raw, `"`)
			source, err := url.Parse(html.UnescapeString(raw))
			acceptanceOK(t, err)
			query = source.Query()
			query.Del("after")
			query.Set("want", "sidebar,typing,presence")
			endpoint := source.Path + "?" + query.Encode() + "&"
			events, status, _ := openEndpointIn(t.Context(), t, b, endpoint, "2", "")
			if status != 200 {
				data, err := os.ReadFile(logs.Name())
				acceptanceOK(t, err)
				t.Fatalf("connect status = %d, want 200; logs: %s", status, data)
			}
			seen := map[string]bool{}
			for len(seen) < 3 {
				e := nextEvent(t, events)
				if (e.name != "sidebar" && e.name != "typing" && e.name != "presence") || e.id != "" {
					t.Fatalf("connect frame = %+v; want id-free sidebar/typing/presence", e)
				}
				seen[e.name] = true
				if e.name == "sidebar" {
					sidebarBadge(t, e.data, view.TopicDOMID(topic.ID), 1)
				}
			}
			// No durable event is appended: only the real member watch wakes this recount.
			r := b.request(t, "POST", path+"/read", url.Values{"cursor": {"2"}})
			r.Header.Set("HX-Request", "true")
			response, body, err := b.send(r)
			acceptanceOK(t, err)
			acceptanceResponse(t, response, body, 204)
			sidebarReload(t, b, path, events)
			stop()
			streamEndsWithin(t, events, time.Second)
			<-poolClosed
			data, err := os.ReadFile(logs.Name())
			acceptanceOK(t, err)
			if !strings.Contains(string(data), `"computation_limit":3`) || !strings.Contains(string(data), `"registration_limit":2`) {
				t.Fatalf("startup did not report computation/registration limits: %s", data)
			}
		})
	}
}
