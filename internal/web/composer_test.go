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
		s.Typing = state
	}}
	setup = append(setup, overrides...)
	h, err := NewHandler("", catalogues, testServices(setup...))
	if err != nil {
		t.Fatal(err)
	}
	return h, state, m, place
}

// composerFragment starts every enhanced composer answer: the state alone,
// never the form or its textarea (#1173).
const composerFragment = `<div id="message-composer-state"`

// Read the successful controls of the real form, not a token issued by the
// test: a page's form, or an enhanced answer's state, whose action is "".
func composerForm(t *testing.T, w *httptest.ResponseRecorder) (string, url.Values) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	for form := range doc.Descendants() {
		if id := attr(form, "id"); id != "message-composer" && id != "message-composer-state" {
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
		for signal := range doc.Descendants() {
			if attr(signal, "id") != "typing-form" {
				continue
			}
			control := find(signal, atom.Input)
			if (control != nil) != values.Has("composer") || (control != nil && attr(control, "value") != values.Get("composer")) {
				t.Fatal("sibling signal generation differs from post controls")
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
					if !hx {
						assertFullConversationPage(t, w.Body.String())
					} else if !strings.HasPrefix(w.Body.String(), composerFragment) {
						t.Fatal("enhanced 422 must render only the composer state")
					}
					action, kept := composerForm(t, w)
					if !hx && action != path {
						t.Fatalf("422 composer posts to %q, want %q", action, path)
					}
					if kept.Has("composer") != (submitted != nil) || (submitted != nil && kept.Get("composer") != *submitted) {
						t.Fatal("422 replaced submitted generation")
					}
					if hx {
						// The draft stays in the page's textarea; the state
						// carries the flag the script copies to aria-invalid.
						state, _ := enhancedForms(t, w.Body.String())
						if _, invalid := attrOK(state, "data-invalid"); !invalid || kept.Has("body") {
							t.Fatal("enhanced 422 lacks the invalid flag or replaces the draft")
						}
					} else if kept.Get("body") != values.Get("body") {
						t.Fatal("422 lost draft")
					}
					if kept.Has("idempotency_key") {
						t.Fatal("422 added a key the request did not send")
					}
				})
			}
		}
	}
}

func TestComposerPostLifecycle(t *testing.T) {
	for _, check := range []string{"fresh", "closing"} {
		for _, mode := range []string{"enhanced-feed", "enhanced-topic", "plain-feed", "plain-topic", "older-feed", "older-topic"} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/retry=%t", check, mode, retry), func(t *testing.T) {
					topic, hx := strings.Contains(mode, "topic"), strings.HasPrefix(mode, "enhanced")
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
						_, values = composerForm(t, w)
						if values.Get("composer") != a || !signal(a) {
							t.Fatal("422 changed or closed generation")
						}
					}
					values.Set("body", "posted")
					w = composerRequest(h, action, values, hx)
					if hx {
						if w.Code != 200 || !strings.HasPrefix(w.Body.String(), composerFragment) {
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

func TestTypingComposerMarkup(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, older := range []bool{false, true} {
			t.Run(fmt.Sprintf("topic=%t/older=%t", topic, older), func(t *testing.T) {
				h, _, _, place := composerHandler(t, topic)
				path := view.ConversationURL("acme", place.Channel, nil)
				if topic {
					path = view.ConversationURL("acme", place.Channel, &place.Topic)
				}
				if older {
					path += "?before=40"
				}
				w := serveForm(h, "GET", path, "live", nil)
				_, controls := composerForm(t, w)
				doc, err := html.Parse(strings.NewReader(w.Body.String()))
				if err != nil {
					t.Fatal(err)
				}
				var signal, message *html.Node
				for n := range doc.Descendants() {
					switch attr(n, "id") {
					case "typing-form":
						signal = n
					case "message-composer":
						message = n
					}
				}
				want := view.ConversationURL("acme", place.Channel, &place.Topic) + "/typing"
				if signal == nil || message == nil || signal.Parent != message.Parent || attr(signal, "hx-post") != want || attr(signal, "hx-trigger") != "typing-input throttle:2s queue:none" || attr(signal, "hx-sync") != "this:drop" || attr(signal, "hx-swap-oob") != "" {
					t.Fatal("wrong signal scope, out-of-band replacement on a full page or request coordination")
				}
				if _, hidden := attrOK(signal, "hidden"); !hidden || attr(find(signal, atom.Input), "value") != controls.Get("composer") || find(signal, atom.Textarea) != nil || find(signal, atom.Button) != nil {
					t.Fatal("signal form exposed controls or a different generation")
				}
				for _, problem := range checkFallbackURLs(doc) {
					t.Error(problem)
				}
				for _, lang := range []string{"en", "ja"} {
					checkHTMXRequests(t, h, doc, lang, true)
				}
				// Latest composers post with htmx to their own feed or topic;
				// older pages keep the plain form.
				wantPost := path
				if older {
					wantPost = ""
				}
				if attr(message, "hx-post") != wantPost {
					t.Fatalf("composer hx-post %q, want %q", attr(message, "hx-post"), wantPost)
				}
				action, posted := composerForm(t, w)
				posted.Set("body", "fragment")
				fragment, err := html.Parse(composerRequest(h, action, posted, true).Body)
				if err != nil {
					t.Fatal(err)
				}
				var renewed int
				for n := range fragment.Descendants() {
					if attr(n, "id") == "typing-form" {
						renewed++
						if attr(n, "hx-swap-oob") != "outerHTML" || attr(n, "hx-post") != want {
							t.Fatal("enhanced fragment does not replace the signal form for the posting topic")
						}
					}
				}
				if renewed != 1 {
					t.Fatalf("enhanced fragment rendered %d signal forms", renewed)
				}
			})
		}
	}
}
