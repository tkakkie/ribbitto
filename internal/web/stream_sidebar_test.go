package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestStreamSidebarBinding(t *testing.T) {
	for _, scope := range []string{"matching", "organisation", "member", "channel", "topic", "no channel"} {
		t.Run(scope, func(t *testing.T) {
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{9}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{3}, JoinedEventSeq: 1}}
			channel, topic := kernel.ID{1}, kernel.ID{6}
			switch scope {
			case "organisation":
				m.Organization.ID = kernel.ID{8}
			case "member":
				m.Member.ID = kernel.ID{4}
			case "channel":
				channel = kernel.ID{2}
			case "topic":
				topic = kernel.ID{7}
			}
			hub, seen, calls := realtime.NewHub(), -1, 0
			p := channelPages{channels: &fakeChannels{}, topics: fakeTopics{}, stream: &Streaming{Hub: hub, Events: organisationLog{},
				Sessions: laterSession{hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: &seen},
				Sidebar: func(got org.Membership, c kernel.ID, selected *kernel.ID) realtime.SidebarSource {
					calls++
					if got != m || c != channel || selected == nil || *selected != topic {
						t.Fatal("sidebar binding lost resolved membership or page selection")
					}
					return nil
				}}}
			handler := middleware.Session(oneSession{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.events(w, r, m) }))
			endpoint := streamEndpoint(t, view.ConversationURL("acme", channel, &topic))
			query := "after=0&" + strings.Replace(strings.TrimPrefix(endpoint, "/organizations/acme/events?"), "want=messages", "want=sidebar", 1)
			wantCalls, wantStatus := 1, 200
			if scope == "no channel" {
				query, wantCalls, wantStatus = "after=0&want=sidebar", 0, 400
			}
			r := httptest.NewRequest("GET", "/events?"+query, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(w, r)
			if w.Code != wantStatus || calls != wantCalls || hub.Connections() != 0 {
				t.Fatalf("status=%d bindings=%d connections=%d", w.Code, calls, hub.Connections())
			}
		})
	}
}
