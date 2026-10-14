package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type fakeTopics struct {
	err     error
	lookups *[][3]kernel.ID
}

// Get records the membership's organisation, the only scope the root uses.
func (f fakeTopics) Get(_ context.Context, m org.Membership, channel, id kernel.ID) (conversation.Topic, error) {
	if f.lookups != nil {
		*f.lookups = append(*f.lookups, [3]kernel.ID{m.Organization.ID, channel, id})
	}
	return conversation.Topic{OrganizationID: m.Organization.ID, ChannelID: channel, ID: id}, f.err
}

func TestTopicHandlersWithoutHistoryRead(t *testing.T) {
	for _, tt := range []struct {
		name, method, query string
		lookupErr           error
		status              int
	}{
		{"post", "POST", "", nil, 303},
		{"invalid bound", "GET", "?before=bad", nil, 400},
		{"malformed query", "GET", "?before=%zz", nil, 400},
		{"unknown topic", "GET", "?before=bad", conversation.ErrTopicNotFound, 404},
		{"lookup failure", "GET", "?before=bad", errors.New("offline"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected := kernel.ID{3}
			var lookups [][3]kernel.ID
			p := channelPages{channels: &fakeChannels{}, posting: testPoster(), topics: fakeTopics{err: tt.lookupErr, lookups: &lookups}}
			// A nil message reader makes any unnecessary history read fail.
			path := view.ConversationURL("acme", kernel.ID{1}, &selected)
			r := httptest.NewRequest(tt.method, path+tt.query, strings.NewReader("body=hello&cursor=0"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
			r.SetPathValue("topicID", "03000000-0000-0000-0000-000000000000")
			w := httptest.NewRecorder()
			p.show(w, r, org.Membership{Organization: org.Organization{ID: kernel.ID{9}, Slug: "acme"}})
			if w.Code != tt.status {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
			if tt.method == "POST" {
				if len(lookups) != 0 || w.Header().Get("Location") != path {
					t.Fatal("post must redirect without a separate topic lookup")
				}
			} else if len(lookups) != 1 || lookups[0] != ([3]kernel.ID{{9}, {1}, {3}}) {
				t.Fatalf("topic lookup scope: %v", lookups)
			}
		})
	}
}

type fakeChannels struct {
	channels                               []conversation.Channel
	createErr, defaultErr, getErr, listErr error
	created                                string
	imageSets                              int
}

func (f *fakeChannels) list(context.Context, org.Membership) ([]conversation.Channel, error) {
	if f.channels != nil {
		return f.channels, f.listErr
	}
	return []conversation.Channel{{ID: kernel.ID{1}, Name: "雑談 <script>alert(1)</script>", IsDefault: true}, {ID: kernel.ID{2}, Name: "Other"}}, f.listErr
}
func (f *fakeChannels) Get(ctx context.Context, m org.Membership, id kernel.ID) (conversation.Channel, error) {
	if f.getErr != nil {
		return conversation.Channel{}, f.getErr
	}
	list, _ := f.list(ctx, m)
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return conversation.Channel{}, conversation.ErrChannelNotFound
}
func (f *fakeChannels) Default(ctx context.Context, m org.Membership) (conversation.Channel, error) {
	list, _ := f.list(ctx, m)
	return list[0], f.defaultErr
}
func (f *fakeChannels) CreateWithImage(_ context.Context, _ org.Membership, name, image string) (conversation.Channel, error) {
	f.created = name
	if err := conversation.ValidateChannelImage(image); err != nil {
		return conversation.Channel{}, err
	}
	return conversation.Channel{ID: kernel.ID{3}, Name: name}, f.createErr
}

func TestChannelHandlers(t *testing.T) {
	current := view.ChannelURL("acme", kernel.ID{1})
	for _, tt := range []struct {
		name, method, path, body, origin, location string
		fake                                       fakeChannels
		status                                     int
		htmx                                       bool
	}{
		{name: "default by flag", method: "GET", path: "/organizations/acme/", status: 303, location: current},
		{name: "missing default", method: "GET", path: "/organizations/acme/", fake: fakeChannels{defaultErr: conversation.ErrChannelNotFound}, status: 500},
		{name: "malformed id", method: "GET", path: "/organizations/acme/channels/bad", status: 404},
		{name: "non-hex id", method: "GET", path: "/organizations/acme/channels/zz000000-0000-0000-0000-000000000000", status: 404},
		{name: "unknown id", method: "GET", path: view.ChannelURL("acme", kernel.ID{9}), status: 404},
		{name: "unknown id with malformed bound", method: "GET", path: view.ChannelURL("acme", kernel.ID{9}) + "?before=bad", status: 404},
		{name: "get failure", method: "GET", path: current, fake: fakeChannels{getErr: errors.New("offline")}, status: 500},
		{name: "list failure", method: "GET", path: current, fake: fakeChannels{listErr: errors.New("offline")}, status: 500},
		{name: "create Japanese", method: "POST", path: "/organizations/acme/channels?name=wrong", body: url.Values{"name": {"雑談"}, "organization_id": {"other"}}.Encode(), status: 303, location: view.ChannelURL("acme", kernel.ID{3})},
		{name: "create failure", method: "POST", path: "/organizations/acme/channels", body: "name=a", fake: fakeChannels{createErr: errors.New("offline")}, status: 500},
		{name: "invalid name", method: "POST", path: "/organizations/acme/channels", body: "name=", fake: fakeChannels{createErr: conversation.ErrInvalidChannelName}, status: 422},
		{name: "duplicate name", method: "POST", path: "/organizations/acme/channels", body: "name=taken", fake: fakeChannels{createErr: conversation.ErrChannelNameTaken}, status: 422},
		// Channel creation is not enhanced: HX changes neither redirects nor validation responses.
		{name: "create Japanese with HX", htmx: true, method: "POST", path: "/organizations/acme/channels?name=wrong", body: url.Values{"name": {"雑談"}, "organization_id": {"other"}}.Encode(), status: 303, location: view.ChannelURL("acme", kernel.ID{3})},
		{name: "invalid name with HX", htmx: true, method: "POST", path: "/organizations/acme/channels", body: "name=", fake: fakeChannels{createErr: conversation.ErrInvalidChannelName}, status: 422},
		{name: "duplicate name with HX", htmx: true, method: "POST", path: "/organizations/acme/channels", body: "name=taken", fake: fakeChannels{createErr: conversation.ErrChannelNameTaken}, status: 422},
		{name: "cross origin", method: "POST", path: "/organizations/acme/channels", body: "name=blocked", origin: "https://attacker.example", status: 403},
		{name: "malformed form", method: "POST", path: "/organizations/acme/channels", body: "name=%zz", status: 400},
		{name: "oversized form", method: "POST", path: "/organizations/acme/channels", body: "name=" + strings.Repeat("a", 65536), status: 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Channels, s.Messages = &tt.fake, fakeMessages{channels: &tt.fake} }))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.htmx {
				r.Header.Set("HX-Request", "true")
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status || w.Header().Get("Location") != tt.location {
				t.Fatalf("status %d, location %q, body %s", w.Code, w.Header().Get("Location"), w.Body.String())
			}
			if tt.status == 403 && tt.fake.created != "" {
				t.Fatal("CSRF reached creation")
			}
			if strings.HasPrefix(tt.name, "create Japanese") && tt.fake.created != "雑談" {
				t.Fatalf("created %q", tt.fake.created)
			}
			if tt.status == 422 {
				assertFullConversationPage(t, w.Body.String())
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				input := find(doc, atom.Input)
				if attr(input, "value") != tt.fake.created || attr(input, "aria-invalid") != "true" {
					t.Fatal("invalid field lost value or state")
				}
				checkFieldError(t, doc, input)
			}
		})
	}
}

func (f *fakeChannels) SetImage(_ context.Context, _ org.Membership, _ kernel.ID, image string) error {
	f.imageSets++
	return conversation.ValidateChannelImage(image)
}

func TestChannelCreationLimit(t *testing.T) {
	for _, language := range []string{"en", "ja"} {
		t.Run(language, func(t *testing.T) {
			fake := &fakeChannels{createErr: conversation.ErrChannelLimit}
			handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Channels, s.Messages = fake, fakeMessages{channels: fake} })
			r := httptest.NewRequest("POST", "/organizations/acme/channels", strings.NewReader("name=kept&image=moon"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Accept-Language", language)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			message := "No more can be created here."
			if language == "ja" {
				message = "ここではこれ以上作成できません。"
			}
			body := w.Body.String()
			if w.Code != 422 || !strings.Contains(body, message) || !strings.Contains(body, `value="kept"`) || !strings.Contains(body, `value="moon" checked`) {
				t.Fatalf("status %d, body %s", w.Code, body)
			}
		})
	}
}

func TestChannelCreationAdmission(t *testing.T) {
	fake := &fakeChannels{}
	limits := middleware.NewChannelCreationRateLimiter(time.Now)
	handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Channels = fake; s.CreateLimits = limits })
	// Session and membership resolution precede admission; denied callers cannot
	// consume the member's budget. The org route suite also proves their 404s.
	r := httptest.NewRequest("POST", "/organizations/unknown/channels", strings.NewReader("name=%zz"))
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("non-member: %d", w.Code)
	}
	for i := range 11 {
		r := httptest.NewRequest("POST", "/organizations/acme/channels", strings.NewReader("name=%zz"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 400
		if i == 10 {
			want = 429
		}
		if w.Code != want || (want == 429 && w.Header().Get("Retry-After") != "60") {
			t.Fatalf("request %d: status %d, retry %q", i, w.Code, w.Header().Get("Retry-After"))
		}
	}
	if fake.created != "" {
		t.Fatal("refusal reached channel creation")
	}
}

// assertChannelListPage checks the list page: the sidebar with channel's topics
// nested, a sidebar-only stream and nothing that reads or posts.
func assertChannelListPage(t *testing.T, body string, channel kernel.ID) {
	t.Helper()
	assertFullConversationPage(t, body)
	stream := `sse-connect="/organizations/acme/events?after=42&amp;want=sidebar&amp;channel=` + strings.TrimPrefix(view.ChannelURL("acme", channel), "/organizations/acme/channels/") + `"`
	nested := `<li id="` + view.ChannelDOMID(channel) + `">`
	if !strings.Contains(body, stream) || !strings.Contains(body, nested) || strings.Count(body, `id="sidebar-topics"`) != 1 || !strings.Contains(body, "<title>lilypads · Acme Corporation · ribbitto</title>") {
		t.Errorf("list page lacks its title, stream %s or nested topics", stream)
	}
	for _, absent := range []string{`id="read-form"`, `id="message-composer"`, `id="message-items"`, `id="typing-status"`, `id="typing-form"`} {
		if strings.Contains(body, absent) {
			t.Errorf("list page renders %s", absent)
		}
	}
}

func TestChannelListPage(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, query string
		channel     kernel.ID
		status      int
	}{
		{"default channel", "", kernel.ID{1}, 200},
		{"named channel", "?channel=" + strings.TrimPrefix(view.ChannelURL("acme", kernel.ID{2}), "/organizations/acme/channels/"), kernel.ID{2}, 200},
		{"unknown channel", "?channel=09000000-0000-0000-0000-000000000000", kernel.ID{}, 404},
		{"malformed channel", "?channel=bad", kernel.ID{}, 404},
		{"empty channel", "?channel=", kernel.ID{}, 404},
		{"repeated channel", "?channel=01000000-0000-0000-0000-000000000000&channel=01000000-0000-0000-0000-000000000000", kernel.ID{}, 404},
		{"malformed query", "?channel=%zz", kernel.ID{}, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewHandler("", catalogues, testServices(asAlice))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/organizations/acme/channels"+tt.query, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
			if tt.status == 200 {
				assertChannelListPage(t, w.Body.String(), tt.channel)
			} else if strings.Contains(w.Body.String(), "Acme") {
				t.Fatal("404 carries organisation data")
			}
		})
	}
}
