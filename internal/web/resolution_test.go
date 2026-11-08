package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
				if tt.status == 409 || tt.status == 422 {
					body := w.Body.String()
					if !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `href="`+path+`"`) {
						t.Fatal("refusal lost its alert or way back")
					}
				}
				if w.Code != tt.status || tt.status == 303 && w.Header().Get("Location") != path {
					t.Fatalf("status %d, redirect %q", w.Code, w.Header().Get("Location"))
				}
			})
		}
	}
}

func TestDefaultResolutionControl(t *testing.T) {
	entry := viewMessage(t.Context(), "acme", conversation.Entry{Message: conversation.Message{ID: kernel.ID{8}}, DefaultTopic: true})
	if got := renderIn(t, "en", view.MessageActionsPage("", entry, "/")); strings.Contains(got, ">Resolve</button>") || entry.ResolveURL != "" {
		t.Fatal("default topic offers Resolve")
	}
}

func TestResolutionDuplicateMessage(t *testing.T) {
	h, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Resolution = fakeResolution{} })
	path := view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2}) + "/resolve"
	w := serveForm(h, "POST", path, "live", url.Values{"message": {"08000000-0000-0000-0000-000000000000", "08000000-0000-0000-0000-000000000000"}})
	if w.Code != 400 {
		t.Fatalf("duplicate message: status %d", w.Code)
	}
}

func TestResolutionAdmission(t *testing.T) {
	for _, action := range []string{"resolve", "reopen"} {
		for _, exhausted := range []string{"pin", "resolution"} {
			t.Run(action+"/"+exhausted, func(t *testing.T) {
				h, _ := newLimitedHandler(t, asAlice, func(s *Services) {
					s.Resolution, s.Pinning = fakeResolution{}, &fakePinning{}
					s.MemberLimits = NewMemberLimits(func() time.Time { return time.Unix(1, 0) })
					limit := s.MemberLimits.Pin()
					if exhausted == "resolution" {
						limit = s.MemberLimits.Resolution()
					}
					for range 10 {
						limit.Allow(kernel.ID{}, kernel.ID{})
					}
				})
				form := url.Values{"message": {"08000000-0000-0000-0000-000000000000"}}
				path := view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2}) + "/" + action
				w := serveForm(h, "POST", path, "live", form)
				want, pinWant := 303, 429
				if exhausted == "resolution" {
					want, pinWant = 429, 303
				}
				if w.Code != want {
					t.Fatalf("resolution status %d, want %d", w.Code, want)
				}
				if want == 429 && w.Header().Get("Retry-After") != "2" {
					t.Fatal("missing retry delay")
				}
				w = serveForm(h, "POST", view.PinURL("acme", kernel.ID{1}, kernel.ID{8})+"/pin", "live", nil)
				if w.Code != pinWant {
					t.Fatalf("pin status %d, want %d", w.Code, pinWant)
				}
			})
		}
	}
}

func TestResolutionSummaryKeyboardRegion(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		markup := renderIn(t, lang, view.TopicResolution(view.ChannelPage{Conclusion: &view.Message{Body: strings.Repeat("long conclusion\n", 40)}}, false))
		label := "Conclusion text"
		if lang == "ja" {
			label = "結論の本文"
		}
		if !strings.Contains(markup, `role="region" tabindex="0" aria-label="`+label+`"`) || !strings.Contains(markup, "max-h-16 overflow-y-auto") {
			t.Fatal("summary has no labelled keyboard region")
		}
	}
}

func TestResolutionMenuScope(t *testing.T) {
	for _, tt := range []struct {
		name       string
		topic      *view.Topic
		resolveURL string
	}{
		{"channel", nil, ""},
		{"default topic", &view.Topic{ID: kernel.ID{2}, IsDefault: true}, ""},
		{"named topic", &view.Topic{ID: kernel.ID{2}}, "/organizations/acme/channels/01000000-0000-0000-0000-000000000000/topics/02000000-0000-0000-0000-000000000000/resolve"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page := feedFixture(t, 1)
			page.Topic = tt.topic
			page.Messages[0].DefaultTopic = false
			page.Messages[0].TopicURL = "/named-topic"
			markup := renderIn(t, "en", view.ChannelScreen("", page))
			if strings.Contains(markup, `id="resolution-form"`) != (tt.topic != nil) || strings.Contains(markup, `data-resolve-url`) != (tt.topic != nil) {
				t.Fatal("Resolve form or URL escaped the topic page")
			}
			if tt.topic != nil && !strings.Contains(markup, `data-resolve-url="`+tt.resolveURL+`"`) {
				t.Fatal("topic list lost its Resolve endpoint")
			}
			for _, item := range []string{renderIn(t, "en", view.MessageItem(page.Messages[0])), renderIn(t, "en", view.LiveMessageItem(page.Messages[0]))} {
				if strings.Contains(item, "data-resolve-url") || !strings.Contains(item, `href="/named-topic"`) {
					t.Fatal("message lost its topic link or retained a per-message Resolve URL")
				}
			}
		})
	}
}
