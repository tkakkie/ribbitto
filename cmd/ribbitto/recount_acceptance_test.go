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
	for _, mode := range []string{"default", "on"} {
		t.Run(mode, func(t *testing.T) {
			saveMutexSampling(t)
			async := mode == "on"
			for key := range recountInputs(t) {
				t.Setenv(key, "stray-invalid")
			}
			if async {
				for key, value := range map[string]string{
					"RIBBITTO_POOL_MAX_CONNS": "4", "RIBBITTO_RECOUNT_WORKERS": "1",
					"RIBBITTO_DB_MAX_CONNECTIONS": "10", "RIBBITTO_DB_RESERVED_CONNECTIONS": "1",
					"RIBBITTO_DB_EXTERNAL_CONNECTIONS": "1", "RIBBITTO_DB_PROCESSES": "1",
					"RIBBITTO_DB_NORMAL_HEADROOM": "2",
				} {
					t.Setenv(key, value)
				}
			} else {
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
			posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, nil)
			_, err = posting.PostToTopic(t.Context(), m, f.Channel.ID, &topic.ID, "unread")
			acceptanceOK(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			owned := make(chan *serviceLifecycle, 1)
			poolClosed := make(chan realtime.RecountStats, 1)
			go func() {
				done <- serveWithLifecycle(ctx, databaseURL.String(), func(ctx context.Context, hub *realtime.Hub, scheduler *realtime.RecountScheduler, closePool func()) *serviceLifecycle {
					l := newServiceLifecycle(ctx, hub, scheduler, func() {
						var stats realtime.RecountStats
						if scheduler != nil {
							stats = scheduler.Stats()
						}
						poolClosed <- stats
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
			var l *serviceLifecycle
			select {
			case l = <-owned:
			case <-time.After(5 * time.Second):
				t.Fatal("serve did not acquire its lifecycle")
			}
			if (l.scheduler != nil) != async {
				t.Fatalf("scheduler constructed = %t, want %t", l.scheduler != nil, async)
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
			if s.Scheduler.Enabled != async || s.Delivery.Enabled != async {
				t.Fatalf("metrics enablement: scheduler=%t delivery=%t, want %t", s.Scheduler.Enabled, s.Delivery.Enabled, async)
			}
			if async && (s.Pool.MaxConns != 4 || s.Scheduler.WorkerLimit != 1 || s.Scheduler.RegistrationLimit != 2) {
				t.Fatalf("opt-in capacity not composed: %+v", s)
			}
			if !async && s.Pool.MaxConns != 7 {
				t.Fatalf("default path changed URL pool maximum: %d, want 7", s.Pool.MaxConns)
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
			if async {
				s = snapshot()
				if s.Scheduler.Completed != 2 || s.Scheduler.Registrations != 1 {
					t.Fatalf("real sidebar delivery bypassed scheduler: %+v", s.Scheduler)
				}
				recountAcceptanceWait(t, func() bool { return snapshot().Delivery.Delivered >= 2 })
				s = snapshot()
				if s.Delivery.Delivered != 2 || s.Delivery.Compute.Count != 2 || s.Database.Queries == 0 {
					t.Fatalf("real sidebar delivery bypassed scheduler/observer/shared pool: %+v", s)
				}
				registration := l.scheduler.Register()
				if registration == nil {
					t.Fatal("second registration refused before capacity")
				}
				release, started, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				t.Cleanup(unblock)
				if !registration.Submit(1, func(ctx context.Context) (realtime.Outgoing, error) {
					close(started)
					<-ctx.Done()
					close(cancelled)
					<-release // Retain the actual worker past cancellation.
					return realtime.Outgoing{}, ctx.Err()
				}) {
					t.Fatal("shutdown gate submission refused")
				}
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("shutdown gate did not start")
				}
				if _, status, _ := openEndpointIn(t.Context(), t, b, endpoint, "2", ""); status != 503 {
					t.Fatalf("registration capacity status = %d, want 503", status)
				}
				s = snapshot()
				if s.Streams.Open != 1 || s.Scheduler.Registrations != 2 || s.Scheduler.RefusedCapacity != 1 {
					t.Fatalf("refusal retained state or missed scheduler: %+v", s)
				}
				cancel()
				select {
				case <-cancelled:
				case <-time.After(5 * time.Second):
					t.Fatal("shutdown did not cancel the composed scheduler")
				}
				select {
				case <-poolClosed:
					t.Fatal("pool closed before actual scheduler return")
				default:
				}
				unblock()
			} else if s = snapshot(); s.Scheduler != (schedulerMetrics{}) || s.Delivery != (deliveryMetrics{}) {
				t.Fatalf("default path constructed recount dependencies: %+v", s)
			}
			stop()
			streamEndsWithin(t, events, time.Second)
			if stats := <-poolClosed; async && (!stats.Closed || stats.RunningWorkers != 0 || stats.WaitingWorkers != 0 || stats.Registrations != 0) {
				t.Fatalf("pool closed before scheduler joined: %+v", stats)
			}
			data, err := os.ReadFile(logs.Name())
			acceptanceOK(t, err)
			delivery := "synchronous"
			if async {
				delivery = "asynchronous"
			}
			if !strings.Contains(string(data), `"delivery":"`+delivery+`"`) {
				t.Fatalf("startup did not report %s delivery: %s", delivery, data)
			}
		})
	}
}
