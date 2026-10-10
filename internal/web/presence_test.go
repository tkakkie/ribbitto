package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/presence"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestPresencePageReconnect(t *testing.T) {
	for _, tt := range []struct{ lang, online, offline string }{
		{"en", "Online", "Offline"}, {"ja", "オンライン", "オフライン"},
	} {
		t.Run(tt.lang, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub := realtime.NewHub()
				state := presence.New(hub)
				closeStream := state.Open(kernel.ID{}, kernel.ID{1})
				catalogues, err := i18n.New(slog.Default())
				if err != nil {
					t.Fatal(err)
				}
				stream := &Streaming{Lifetime: t.Context(), Hub: hub, Presence: state, Events: quietLog{}, Authorizer: streamAllow(true),
					Owners:   map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestPresence: NewPresenceOwner(t.Context(), state)},
					Sessions: laterSession{hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: new(int)}}
				handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Stream = stream }))
				if err != nil {
					t.Fatal(err)
				}
				request := func(path string) *http.Request {
					r := httptest.NewRequest(http.MethodGet, path, nil)
					r.Header.Set("Accept-Language", tt.lang)
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					return r
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, request(view.ChannelURL("acme", kernel.ID{1})+"/members"))
				doc, err := html.Parse(strings.NewReader(w.Body.String()))
				if err != nil {
					t.Fatal(err)
				}
				if problems := checkMarkup(doc, true); w.Code != 200 || len(problems) != 0 {
					t.Fatalf("page %d: %v", w.Code, problems)
				}
				endpoint, indicators := "", 0
				for n := range doc.Descendants() {
					if attr(n, "id") == "organization-stream" {
						endpoint = attr(n, "sse-connect")
					}
					if strings.HasPrefix(attr(n, "id"), "presence-") {
						indicators++
						label := tt.offline
						if attr(n, "id") == view.PresenceDOMID(kernel.ID{1}) {
							label = tt.online
						}
						if strings.TrimSpace(text(n)) != label || attr(n, "hx-swap-oob") != "" {
							t.Fatalf("indicator: %s", text(n))
						}
					}
				}
				u, err := url.Parse(endpoint)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := state.Read(kernel.ID{}, []kernel.ID{{1}})
				if err != nil {
					t.Fatal(err)
				}
				if indicators != 2 || !strings.Contains(w.Body.String(), "bg-success") || u.Query().Get("want") != "sidebar,presence" || u.Query().Get("presence-after") != presenceToken(snapshot.Token) {
					t.Fatal("page did not render snapshot state and token")
				}
				closeStream()
				time.Sleep(31 * time.Second)
				for _, mode := range []string{"reconnect", "denied", "no interest", "reset"} {
					func() {
						target := *u
						q := target.Query()
						stream.Authorizer = streamAllow(mode != "denied")
						if mode == "no interest" {
							q.Set("want", "sidebar")
						}
						if mode == "reset" {
							q.Set("presence-after", "another-process:0")
						}
						target.RawQuery = q.Encode()
						r := request(target.String())
						ctx, cancel := context.WithCancel(r.Context())
						defer cancel()
						timer := time.AfterFunc(time.Second, cancel)
						defer timer.Stop()
						live := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
						handler.ServeHTTP(live, r.WithContext(ctx))
						body := live.Body.String()
						if live.Code != 200 || strings.Contains(body, "id:") || strings.Contains(body, "event: presence") != (mode == "reconnect") || strings.Contains(body, "event: reset") != (mode == "reset") {
							t.Fatalf("stream: %d %s", live.Code, body)
						}
						if mode == "reconnect" && (!strings.Contains(body, tt.offline) || strings.Contains(body, tt.online) || !strings.Contains(body, view.PresenceDOMID(kernel.ID{1})) || !strings.Contains(body, `hx-swap-oob="outerHTML"`)) {
							t.Fatalf("offline replacement: %s", body)
						}
					}()
				}
			})
		})
	}
}
