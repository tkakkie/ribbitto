package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type fakeResolution struct{ err error }

func (f fakeResolution) Resolve(_ context.Context, m org.Membership, channel, topic, message kernel.ID) error {
	if m.Organization.Slug != "acme" || channel != (kernel.ID{1}) || topic != (kernel.ID{2}) || message != (kernel.ID{8}) {
		panic("resolution scope changed")
	}
	return f.err
}
func (f fakeResolution) Reopen(ctx context.Context, m org.Membership, channel, topic kernel.ID) error {
	return f.Resolve(ctx, m, channel, topic, kernel.ID{8})
}

func TestResolutionRoutes(t *testing.T) {
	for _, action := range []string{"resolve", "reopen"} {
		for _, tt := range []struct {
			name   string
			err    error
			status int
		}{
			{"success", nil, 303},
			{"cannot write", org.ErrNotFound, 404},
			{"unknown topic", conversation.ErrTopicNotFound, 404},
			{"other channel message", conversation.ErrMessageNotFound, 404},
			{"default", conversation.ErrDefaultTopicResolution, 422},
			{"moved", conversation.ErrConclusionConflict, 409},
		} {
			t.Run(action+"/"+tt.name, func(t *testing.T) {
				h, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Resolution = fakeResolution{tt.err} })
				path := view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2})
				r := httptest.NewRequest("POST", path+"/"+action, strings.NewReader("message=08000000-0000-0000-0000-000000000000"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tt.status || tt.status == 303 && w.Header().Get("Location") != path {
					t.Fatalf("status %d, redirect %q", w.Code, w.Header().Get("Location"))
				}
			})
		}
	}
}

func TestDefaultResolutionControl(t *testing.T) {
	entry := viewMessage("acme", conversation.Entry{Message: conversation.Message{ID: kernel.ID{8}}, DefaultTopic: true})
	if got := renderIn(t, "en", view.MessageActionsPage("", entry, "/")); strings.Contains(got, ">Resolve</button>") || entry.ResolveURL != "" {
		t.Fatal("default topic offers Resolve")
	}
}
