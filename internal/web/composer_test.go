package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type composerMember struct {
	oneOrganisation
	membership org.Membership
}

func (a composerMember) Member(context.Context, *identity.Account, string) (org.Membership, error) {
	return a.membership, nil
}

func composerHandler(t *testing.T, topic bool, overrides ...func(*Services)) (http.Handler, *typing.State, org.Membership, typing.Place) {
	t.Helper()
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{7}, Slug: "acme"}, Member: org.Member{ID: kernel.ID{8}, Handle: "alice"}}
	c := conversation.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}}
	snapshot := conversation.ChannelPage{Current: c, EventCursor: new(int64(42))}
	place := typing.Place{Channel: c.ID, Topic: c.DefaultTopicID}
	if topic {
		snapshot.Topic = &conversation.Topic{ID: kernel.ID{3}}
		place.Topic = snapshot.Topic.ID
	}
	state := typing.New(realtime.NewHub())
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	setup := []func(*Services){asAlice, func(s *Services) {
		s.Authz = composerMember{membership: m}
		s.Messages = sidebarReader{page: snapshot}
		s.Channels = &fakeChannels{channels: []conversation.Channel{c}}
		s.Typing, s.SignalLimits = state, middleware.NewMemberRateLimiter(time.Now)
	}}
	setup = append(setup, overrides...)
	h, err := NewHandler("", catalogues, testServices(setup...))
	if err != nil {
		t.Fatal(err)
	}
	return h, state, m, place
}

// Read the successful controls of the real form, not a token issued by the test.
func composerForm(t *testing.T, w *httptest.ResponseRecorder) (string, url.Values) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	for form := range doc.Descendants() {
		if attr(form, "id") != "message-composer" {
			continue
		}
		values := url.Values{}
		for n := range form.Descendants() {
			if n.DataAtom == atom.Input {
				values.Add(attr(n, "name"), attr(n, "value"))
			}
			if n.DataAtom == atom.Textarea {
				values.Add(attr(n, "name"), text(n))
			}
		}
		return attr(form, "action"), values
	}
	t.Fatal("missing composer form")
	return "", nil
}

func composerRequest(h http.Handler, path string, values url.Values, hx bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestComposerIssuanceScope(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, suffix := range []string{"", "?before=40"} {
			t.Run(fmt.Sprintf("topic=%t/%s", topic, suffix), func(t *testing.T) {
				h, state, m, place := composerHandler(t, topic, func(s *Services) {
					s.Channels = &fakeChannels{getErr: errors.New("extra channel lookup")}
					s.Topics = fakeTopics{err: errors.New("extra topic lookup")}
				})
				// The snapshot deliberately resolves IDs different from the URL.
				path := view.ConversationURL("acme", kernel.ID{9}, nil)
				if topic {
					path = view.ConversationURL("acme", kernel.ID{9}, &kernel.ID{10})
				}
				w := serveForm(h, "GET", path+suffix, "live", nil)
				if w.Code != 200 {
					t.Fatalf("render status %d", w.Code)
				}
				_, values := composerForm(t, w)
				token := values.Get("composer")
				if len(values["composer"]) != 1 || token == "" {
					t.Fatal("missing issued control")
				}
				defer state.Open(m.Organization.ID, m.Member.ID, place.Channel)()
				if !state.Signal(m.Organization.ID, place, typing.Typist{Member: m.Member.ID}, token, time.Now()) {
					t.Fatal("generation has wrong resolved scope")
				}
			})
		}
	}
}

func TestComposerValidationPreservation(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			for _, submitted := range []*string{nil, new(""), new(" invalid<&\" generation ")} {
				t.Run(fmt.Sprintf("topic=%t/hx=%t/value=%q", topic, hx, func() string {
					if submitted == nil {
						return "missing"
					}
					return *submitted
				}()), func(t *testing.T) {
					h, _, _, place := composerHandler(t, topic)
					path := view.ConversationURL("acme", place.Channel, nil)
					if topic {
						path = view.ConversationURL("acme", place.Channel, &place.Topic)
					}
					values := url.Values{"body": {"\n\n"}}
					if submitted != nil {
						values.Set("composer", *submitted)
					}
					w := composerRequest(h, path+"?composer=query-must-not-win", values, hx)
					if w.Code != 422 {
						t.Fatalf("status %d", w.Code)
					}
					if !hx || topic {
						assertFullConversationPage(t, w.Body.String())
					}
					_, kept := composerForm(t, w)
					if kept.Has("composer") != (submitted != nil) || (submitted != nil && kept.Get("composer") != *submitted) {
						t.Fatal("422 replaced submitted generation")
					}
					if kept.Get("body") != values.Get("body") {
						t.Fatal("422 lost draft")
					}
				})
			}
		}
	}
}

func TestComposerPostLifecycle(t *testing.T) {
	for _, check := range []string{"fresh", "closing"} {
		for _, mode := range []string{"enhanced-feed", "plain-feed", "topic", "older-feed", "older-topic"} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/retry=%t", check, mode, retry), func(t *testing.T) {
					topic, hx := strings.Contains(mode, "topic"), mode == "enhanced-feed"
					h, state, m, place := composerHandler(t, topic)
					path := view.ConversationURL("acme", place.Channel, nil)
					if topic {
						path = view.ConversationURL("acme", place.Channel, &place.Topic)
					}
					initial := path
					if strings.HasPrefix(mode, "older") {
						initial += "?before=40"
					}
					w := serveForm(h, "GET", initial, "live", nil)
					if w.Code != 200 {
						t.Fatalf("initial status %d", w.Code)
					}
					action, values := composerForm(t, w)
					a := values.Get("composer")
					defer state.Open(m.Organization.ID, m.Member.ID, place.Channel)()
					signal := func(token string) bool {
						return state.Signal(m.Organization.ID, place, typing.Typist{Member: m.Member.ID}, token, time.Now())
					}
					if !signal(a) {
						t.Fatal("real form generation refused before post")
					}
					if retry {
						values.Set("body", "\n\n")
						w = composerRequest(h, action, values, hx)
						if w.Code != 422 {
							t.Fatalf("failure status %d", w.Code)
						}
						action, values = composerForm(t, w)
						if values.Get("composer") != a || !signal(a) {
							t.Fatal("422 changed or closed generation")
						}
					}
					values.Set("body", "posted")
					w = composerRequest(h, action, values, hx)
					if hx {
						if w.Code != 200 {
							t.Fatalf("enhanced status %d", w.Code)
						}
					} else {
						if w.Code != 303 || w.Header().Get("Location") != path {
							t.Fatalf("redirect status %d", w.Code)
						}
						w = serveForm(h, "GET", w.Header().Get("Location"), "live", nil)
						if w.Code != 200 {
							t.Fatalf("redirect page status %d", w.Code)
						}
					}
					if check == "closing" {
						if len(state.Read(m.Organization.ID, place, kernel.ID{99}).Typists) != 0 || signal(a) {
							t.Fatal("successful post did not close rendered generation")
						}
					} else {
						_, fresh := composerForm(t, w)
						b := fresh.Get("composer")
						if b == "" || b == a || !signal(b) {
							t.Fatal("success did not render a fresh usable generation")
						}
					}
				})
			}
		}
	}
}
