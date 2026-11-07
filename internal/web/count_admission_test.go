package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestCountAdmissionRoutes(t *testing.T) {
	feed := view.ChannelURL("acme", kernel.ID{1})
	topic := feed + "/topics/31000000-0000-0000-0000-000000000000"
	type route struct {
		method, path, form string
		hx                 bool
		status             int
		counts             bool
	}
	var routes []route
	for _, path := range []string{feed, topic, feed + "?before=9", topic + "?before=9", feed + "?around=7", topic + "?around=7", feed + "/members", topic + "/members", feed + "/overview", topic + "/overview", "/organizations/acme/channels"} {
		for _, method := range []string{"GET", "HEAD"} {
			routes = append(routes, route{method, path, "", false, 200, true})
		}
	}
	routes = append(routes,
		route{"POST", "/organizations/acme/channels", "image=invalid", false, 422, true},
		route{"POST", feed, "body=", false, 422, true}, route{"POST", topic, "body=", false, 422, true},
		route{"POST", feed + "/branch", "", false, 422, true},
		route{"POST", feed + "/overview", "image=invalid", false, 422, true}, route{"POST", topic + "/overview", "image=invalid", false, 422, true},
		route{"GET", "/organizations/acme/", "", false, 303, false},
		route{"POST", feed, "body=hello", false, 303, false}, route{"POST", feed, "body=", true, 422, false},
		route{"POST", feed + "/branch", "", true, 422, false},
		route{"POST", feed + "/branch", "from=31000000-0000-0000-0000-000000000000&message=08000000-0000-0000-0000-000000000000&name=design", false, 303, false},
		route{"POST", feed + "/overview", "image=", false, 303, false},
		route{"POST", "/organizations/acme/channels", "name=new", false, 303, false},
		route{"POST", feed + "/read", "cursor=0", true, 204, false},
		route{"POST", "/organizations/acme/send-key", "send_key=enter", false, 303, false},
		route{"GET", "/organizations/acme/mention-candidates?q=a", "", false, 200, false},
		route{"POST", "/organizations/acme/sidebar/resync", "", false, 204, false},
		route{"POST", topic + "/typing", "composer=invalid", false, 204, false},
		route{"POST", view.PinURL("acme", kernel.ID{1}, kernel.ID{8}) + "/pin", "", false, 303, false},
	)
	cat, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range routes {
		for _, exhausted := range []string{"none", "member", "shared"} {
			t.Run(tc.method+tc.path+tc.form+exhausted, func(t *testing.T) {
				before, around := []*int64{}, []int64{}
				s := testServices(asAlice, func(s *Services) {
					reader := populatedMessages()
					reader.before, reader.around = &before, &around
					s.Messages = reader
					s.Reading = &formReading{}
					s.Pinning = &fakePinning{}
					s.Typing = typing.New(realtime.NewHub())
				})
				s.Stream = &Streaming{Lifetime: t.Context(), RecountLimit: realtime.NewRecountLimit(2)}
				// Two slots represent a pool of four; fill both for shared exhaustion.
				if exhausted == "shared" {
					for range 2 {
						release, err := s.Stream.RecountLimit.Acquire(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						defer release()
					}
				}
				if exhausted == "member" {
					s.Stream = nil // The member cap also applies without a hub.
					release, ok := s.MemberLimits.count.Admit(kernel.ID{}, kernel.ID{})
					if !ok {
						t.Fatal("member admission")
					}
					defer release()
				}
				h, err := NewHandler("", cat, s)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 { // A completed count must release both budgets.
					r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					if tc.hx {
						r.Header.Set("HX-Request", "true")
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					want := tc.status
					if tc.counts && exhausted == "member" {
						want = 429
					}
					if tc.counts && exhausted == "shared" {
						want = 503
					}
					if (len(before)+len(around) > 0) != (tc.counts && exhausted == "none") {
						t.Fatal("count reader called without admission, or never called")
					}
					if w.Code != want {
						t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
					}
					if tc.counts && exhausted != "none" && (w.Header().Get("Retry-After") == "" || w.Body.Len() != 0) {
						t.Fatal("rejection headers/body")
					}
				}
			})
		}
	}
}

type blockedCountReader struct {
	MessageReader
	entered chan struct{}
}

func (b blockedCountReader) Page(ctx context.Context, _ org.Membership, _ kernel.ID, _ *kernel.ID, _ *int64) (conversation.ChannelPage, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return conversation.ChannelPage{}, ctx.Err()
}

func TestCountAdmissionFairnessAndRelease(t *testing.T) {
	for _, slots := range []int{1, 2} {
		synctest.Test(t, func(t *testing.T) {
			// Pools of two/four have one/two shared slots; every member has one slot.
			p := channelPages{countLimit: middleware.NewCountRateLimiter(time.Now), stream: &Streaming{RecountLimit: realtime.NewRecountLimit(slots)}}
			a := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
			b := a
			b.Member.ID = kernel.ID{3}
			entered := make(chan struct{}, 2)
			p.messages = blockedCountReader{entered: entered}
			start := func(m org.Membership) (context.CancelFunc, <-chan struct{}) {
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() {
					defer close(done)
					p.render(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx), m, kernel.ID{1}, 200, view.ChannelPage{}, nil)
				}()
				<-entered
				return cancel, done
			}
			cancelA, doneA := start(a)
			defer cancelA()
			refuse := func(m org.Membership, want int) {
				t.Helper()
				w := httptest.NewRecorder()
				started := time.Now()
				release, ok := p.admitCount(w, httptest.NewRequest("GET", "/", nil), m)
				if ok {
					release()
					t.Fatal("unexpected admission")
				}
				if w.Code != want || want == 429 && !time.Now().Equal(started) {
					t.Fatalf("status %d want %d; waited %s", w.Code, want, time.Since(started))
				}
			}
			stopOther := func() {}
			if slots == 2 {
				cancelB, doneB := start(b)
				stopOther = func() { cancelB(); <-doneB }
				defer cancelB()
				b.Member.ID = kernel.ID{4}
			}
			refuse(a, 429) // Member refusal wins even when every shared slot is held.
			refuse(b, 503)
			// synctest.Wait proves cancellation happens during the bounded wait.
			ctx, cancel := context.WithCancel(t.Context())
			waiting := make(chan struct{})
			go func() {
				defer close(waiting)
				if release, ok := p.admitCount(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx), b); ok {
					release()
					t.Error("canceled waiter admitted")
				}
			}()
			synctest.Wait()
			cancel()
			<-waiting
			stopOther()
			cancelA()
			<-doneA
			for _, member := range []org.Membership{a, b} {
				release, ok := p.admitCount(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), member)
				if !ok {
					t.Fatalf("member %v leaked admission", member.Member.ID)
				}
				release()
			}
		})
	}
}

func TestCountRejectionNavigation(t *testing.T) {
	cat, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{429, 503} {
		for _, lang := range []string{"en", "ja"} {
			p := channelPages{countLimit: middleware.NewCountRateLimiter(time.Now), stream: &Streaming{RecountLimit: realtime.NewRecountLimit(1)}, pages: &pageRenderer{stylesheetURL: func() (string, error) { return "", nil }}}
			var release func()
			if status == 429 {
				release, _ = p.countLimit.Admit(kernel.ID{}, kernel.ID{})
			} else {
				release, err = p.stream.RecountLimit.Acquire(t.Context())
				if err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(cat.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.admitCount(w, r, org.Membership{}) })))
			for _, method := range []string{"GET", "HEAD", "POST"} {
				for _, mode := range []string{"navigate", "cors", "htmx"} {
					r, err := http.NewRequestWithContext(t.Context(), method, server.URL, strings.NewReader(""))
					if err != nil {
						t.Fatal(err)
					}
					r.Header.Set("Accept-Language", lang)
					r.Header.Set("Accept", "text/html")
					r.Header.Set("Sec-Fetch-Mode", map[string]string{"navigate": "navigate", "cors": "cors", "htmx": "navigate"}[mode])
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					if mode == "htmx" {
						r.Header.Set("HX-Request", "true")
					}
					response, err := server.Client().Do(r)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					closeErr := response.Body.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("reading response: %v, close: %v", err, closeErr)
					}
					if response.StatusCode != status || response.Header.Get("Retry-After") != "1" {
						t.Fatalf("response: %v", response)
					}
					title := map[string]string{"en": "Server busy", "ja": "サーバーが混み合っています"}[lang]
					if mode == "navigate" && (!strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || method != "HEAD" && (!strings.Contains(string(body), `<html lang="`+lang+`"`) || status == 503 && !strings.Contains(string(body), title))) {
						t.Fatal("missing localized HTML page or headers")
					}

					if (method == "HEAD" || mode != "navigate") && len(body) != 0 {
						t.Fatal("unexpected body")
					}
				}
			}
			server.Close()
			release()
		}
	}
}
