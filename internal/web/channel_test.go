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

	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type fakeTopics struct {
	err     error
	lookups *[][3]domain.ID
}

func (f fakeTopics) GetTopic(_ context.Context, org, channel, id domain.ID) (domain.Topic, error) {
	if f.lookups != nil {
		*f.lookups = append(*f.lookups, [3]domain.ID{org, channel, id})
	}
	return domain.Topic{OrganizationID: org, ChannelID: channel, ID: id}, f.err
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
		{"unknown topic", "GET", "?before=bad", topic.ErrNotFound, 404},
		{"lookup failure", "GET", "?before=bad", errors.New("offline"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected := domain.ID{3}
			var lookups [][3]domain.ID
			p := channelPages{service: &fakeChannels{}, posting: testPoster(), topics: fakeTopics{err: tt.lookupErr, lookups: &lookups}}
			// A nil message reader makes any unnecessary history read fail.
			path := view.ConversationURL("acme", domain.ID{1}, &selected)
			r := httptest.NewRequest(tt.method, path+tt.query, strings.NewReader("body=hello"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
			r.SetPathValue("topicID", "03000000-0000-0000-0000-000000000000")
			w := httptest.NewRecorder()
			p.show(w, r, org.Membership{Organization: org.Organization{ID: domain.ID{9}, Slug: "acme"}})
			if w.Code != tt.status {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
			if tt.method == "POST" {
				if len(lookups) != 0 || w.Header().Get("Location") != path {
					t.Fatal("post must redirect without a separate topic lookup")
				}
			} else if len(lookups) != 1 || lookups[0] != ([3]domain.ID{{9}, {1}, {3}}) {
				t.Fatalf("topic lookup scope: %v", lookups)
			}
		})
	}
}

type fakeChannels struct {
	createErr, defaultErr, getErr, listErr error
	created                                string
}

func (f *fakeChannels) List(context.Context, org.Membership) ([]domain.Channel, error) {
	return []domain.Channel{{ID: domain.ID{1}, Name: "雑談 <script>alert(1)</script>", IsDefault: true}, {ID: domain.ID{2}, Name: "Other"}}, f.listErr
}
func (f *fakeChannels) Get(ctx context.Context, m org.Membership, id domain.ID) (domain.Channel, error) {
	if f.getErr != nil {
		return domain.Channel{}, f.getErr
	}
	list, _ := f.List(ctx, m)
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return domain.Channel{}, channel.ErrNotFound
}
func (f *fakeChannels) Default(ctx context.Context, m org.Membership) (domain.Channel, error) {
	list, _ := f.List(ctx, m)
	return list[0], f.defaultErr
}
func (f *fakeChannels) Create(_ context.Context, _ org.Membership, name string) (domain.Channel, error) {
	f.created = name
	return domain.Channel{ID: domain.ID{3}, Name: name}, f.createErr
}

func TestChannelHandlers(t *testing.T) {
	current := view.ChannelURL("acme", domain.ID{1})
	for _, tt := range []struct {
		name, method, path, body, origin, location string
		fake                                       fakeChannels
		status                                     int
		htmx                                       bool
	}{
		{name: "default by flag", method: "GET", path: "/organizations/acme/", status: 303, location: current},
		{name: "missing default", method: "GET", path: "/organizations/acme/", fake: fakeChannels{defaultErr: channel.ErrNotFound}, status: 500},
		{name: "malformed id", method: "GET", path: "/organizations/acme/channels/bad", status: 404},
		{name: "non-hex id", method: "GET", path: "/organizations/acme/channels/zz000000-0000-0000-0000-000000000000", status: 404},
		{name: "unknown id", method: "GET", path: view.ChannelURL("acme", domain.ID{9}), status: 404},
		{name: "unknown id with malformed bound", method: "GET", path: view.ChannelURL("acme", domain.ID{9}) + "?before=bad", status: 404},
		{name: "get failure", method: "GET", path: current, fake: fakeChannels{getErr: errors.New("offline")}, status: 500},
		{name: "list failure", method: "GET", path: current, fake: fakeChannels{listErr: errors.New("offline")}, status: 500},
		{name: "create Japanese", method: "POST", path: "/organizations/acme/channels?name=wrong", body: url.Values{"name": {"雑談"}, "organization_id": {"other"}}.Encode(), status: 303, location: view.ChannelURL("acme", domain.ID{3})},
		{name: "create failure", method: "POST", path: "/organizations/acme/channels", body: "name=a", fake: fakeChannels{createErr: errors.New("offline")}, status: 500},
		{name: "invalid name", method: "POST", path: "/organizations/acme/channels", body: "name=", fake: fakeChannels{createErr: channel.ErrInvalidName}, status: 422},
		{name: "duplicate name", method: "POST", path: "/organizations/acme/channels", body: "name=taken", fake: fakeChannels{createErr: channel.ErrNameTaken}, status: 422},
		// Channel creation is not enhanced: HX changes neither redirects nor validation responses.
		{name: "create Japanese with HX", htmx: true, method: "POST", path: "/organizations/acme/channels?name=wrong", body: url.Values{"name": {"雑談"}, "organization_id": {"other"}}.Encode(), status: 303, location: view.ChannelURL("acme", domain.ID{3})},
		{name: "invalid name with HX", htmx: true, method: "POST", path: "/organizations/acme/channels", body: "name=", fake: fakeChannels{createErr: channel.ErrInvalidName}, status: 422},
		{name: "duplicate name with HX", htmx: true, method: "POST", path: "/organizations/acme/channels", body: "name=taken", fake: fakeChannels{createErr: channel.ErrNameTaken}, status: 422},
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
