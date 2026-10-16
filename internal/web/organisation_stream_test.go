package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

const streamChannel = "01000000-0000-0000-0000-000000000000"
const streamTopic = "06000000-0000-0000-0000-000000000000"

func TestOrganisationStreamValidation(t *testing.T) {
	for _, query := range []string{
		"", "want=unknown", "want=sidebar,sidebar", "want=", "want=sidebar&want=messages",
		"want=pins", "want=pins,pins&channel=" + streamChannel, "want=messages", "want=typing", "want=sidebar", "want=sidebar&panel=members", "want=sidebar&topic=" + streamTopic,
		"want=presence&presence-after=instance:0&channel=" + streamChannel + "&topic=" + streamTopic,
		"want=sidebar&channel=" + streamChannel + "&topic=bad", "want=sidebar&channel=" + streamChannel + "&panel=members",
		"want=presence", "want=presence&presence-after=", "want=sidebar&channel=bad",
		"want=messages&channel=" + streamChannel + "&topic=bad", "want=sidebar&channel=" + streamChannel + "&channel=" + streamChannel,
		"want=sidebar&after=%zz", "want=sidebar&after=0;broken", "want=sidebar&unexpected=1",
		"want=sidebar&presence-after=a&presence-after=b", "want=messages&channel=" + streamChannel + "&topic=" + streamTopic + "&topic=" + streamTopic,
	} {
		t.Run(query, func(t *testing.T) { organisationStream(t, query, realtime.Event{}, false, nil, 400, false, 0) })
	}
	for _, want := range []string{"pins", "sidebar", "messages", "typing", "sidebar,messages,typing", "presence&presence-after=instance:0"} {
		t.Run(want, func(t *testing.T) {
			query := "want=" + want + "&channel=" + streamChannel
			organisationStream(t, query, realtime.Event{}, false, conversation.ErrChannelNotFound, 404, false, 0)
		})
	}
	for _, query := range []string{"want=sidebar&channel=" + streamChannel, "want=sidebar&channel=" + streamChannel + "&topic=" + streamTopic, "want=presence&presence-after=instance:0", "want=sidebar,messages,typing&channel=" + streamChannel} {
		t.Run(query, func(t *testing.T) { organisationStream(t, query, realtime.Event{}, false, nil, 200, false, 0) })
	}
}

type organisationLog struct{ event realtime.Event }

func (l organisationLog) EventsAfter(_ context.Context, _ kernel.ID, after int64, _ int) ([]realtime.Event, error) {
	if after > 0 || l.event.Seq == 0 {
		return nil, io.EOF
	}
	return []realtime.Event{l.event}, nil
}

type streamMessages struct {
	fakeMessages
	countingMessages
}

func (m streamMessages) One(ctx context.Context, member org.Membership, channel kernel.ID, seq int64) (conversation.Entry, error) {
	return m.countingMessages.One(ctx, member, channel, seq)
}
func (m streamMessages) Many(ctx context.Context, member org.Membership, channel kernel.ID, ids []kernel.ID) ([]conversation.Entry, error) {
	return m.countingMessages.Many(ctx, member, channel, ids)
}

type streamAllow bool

func (a streamAllow) MayReceive(context.Context, kernel.ID, string, realtime.Event) (bool, error) {
	return bool(a), nil
}

// Only one scope differs in each negative fixture; an allowing authorizer and
// unscoped fake log ensure neither can mask a missing subscription predicate.
func TestOrganisationStreamScopes(t *testing.T) {
	for _, tt := range []struct {
		name, want                   string
		organization, channel, topic kernel.ID
		deny, sent                   bool
		renders                      int32
	}{
		{"matching feed", "messages", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, true, 1},
		{"matching", "messages", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, true, 1},
		{"organisation", "messages", kernel.ID{9}, kernel.ID{1}, kernel.ID{6}, false, false, 0},
		{"channel", "messages", kernel.ID{}, kernel.ID{2}, kernel.ID{6}, false, false, 0},
		{"topic", "messages", kernel.ID{}, kernel.ID{1}, kernel.ID{7}, false, false, 0},
		{"sidebar selection", "sidebar", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, false, 0},
		{"interest", "sidebar", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, false, 0},
		{"typing", "typing", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, false, 0},
		{"presence", "presence&presence-after=instance:0", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, false, false, 0},
		{"interest grants nothing", "messages", kernel.ID{}, kernel.ID{1}, kernel.ID{6}, true, false, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query := "want=" + tt.want + "&channel=" + streamChannel
			if tt.name == "sidebar selection" || (tt.want == "messages" && tt.name != "matching feed") || tt.want == "typing" {
				query += "&topic=" + streamTopic
			}
			e := realtime.Event{OrganizationID: tt.organization, ChannelID: tt.channel, Topics: []kernel.ID{tt.topic}, Seq: 1, Kind: conversation.KindPosted}
			organisationStream(t, query, e, tt.deny, nil, 200, tt.sent, tt.renders)
		})
	}
}

func organisationStream(t *testing.T, query string, event realtime.Event, deny bool, channelErr error, status int, sent bool, renders int32) {
	t.Helper()
	hub := realtime.NewHub()
	hub.Raise(kernel.ID{}, 2) // Let the finite log reach its terminating read after one batch.
	seen := -1
	var reads atomic.Int32
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
		s.Channels = &fakeChannels{getErr: channelErr}
		s.Messages = streamMessages{countingMessages: countingMessages{calls: &reads}}
		s.Stream = &Streaming{Lifetime: t.Context(), Hub: hub, Events: organisationLog{event}, Authorizer: streamAllow(!deny),
			Sessions: laterSession{hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: &seen}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/organizations/acme/events?after=0&"+query, nil)
	r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(w, r)
	if w.Code != status || strings.Contains(w.Body.String(), "event: message") != sent || reads.Load() != renders || hub.Connections() != 0 {
		t.Fatalf("status %d, body %q, renders %d, connections %d", w.Code, w.Body.String(), reads.Load(), hub.Connections())
	}
	if status != 200 && (seen != -1 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream")) {
		t.Fatal("refused request registered or sent SSE headers")
	}
}

// Scope fixtures differ in one field; the allowing channel fake cannot mask a
// topic lookup that loses the organisation, channel or selected-topic scope.
type sidebarScopeTopics struct{ scope [3]kernel.ID }

func (f sidebarScopeTopics) Get(_ context.Context, m org.Membership, channel, topic kernel.ID) (conversation.Topic, error) {
	if [3]kernel.ID{m.Organization.ID, channel, topic} != f.scope {
		return conversation.Topic{}, conversation.ErrTopicNotFound
	}
	return conversation.Topic{ID: topic}, nil
}

func TestSidebarStreamScope(t *testing.T) {
	for _, tt := range []struct {
		name   string
		scope  [3]kernel.ID
		status int
	}{
		{"matching", [3]kernel.ID{{9}, {1}, {6}}, 200},
		{"organisation", [3]kernel.ID{{8}, {1}, {6}}, 404},
		{"channel", [3]kernel.ID{{9}, {2}, {6}}, 404},
		{"topic", [3]kernel.ID{{9}, {1}, {7}}, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub, seen := realtime.NewHub(), -1
			var reads atomic.Int32
			p := channelPages{channels: &fakeChannels{}, topics: sidebarScopeTopics{tt.scope},
				stream: &Streaming{Lifetime: t.Context(), Hub: hub, Events: firstRead{&reads}, Sessions: laterSession{
					hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: &seen}}}
			handler := middleware.Session(oneSession{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p.events(w, r, org.Membership{Organization: org.Organization{ID: kernel.ID{9}, Slug: "acme"}})
			}))
			r := httptest.NewRequest("GET", "/events?after=0&want=sidebar&channel="+streamChannel+"&topic="+streamTopic, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(w, r)
			if w.Code != tt.status || (tt.status == 404 && (seen != -1 || reads.Load() != 0)) || hub.Connections() != 0 {
				t.Fatalf("status %d, registered %d, reads %d; want %d before any registration or delivery on refusal", w.Code, seen, reads.Load(), tt.status)
			}
		})
	}
}

func TestPinsInterestMapping(t *testing.T) {
	for _, topic := range []string{"", "&topic=" + streamTopic} {
		sub, ok := streamScope(httptest.NewRequest("GET", "/events?want=pins&channel="+streamChannel+topic, nil))
		if !ok || len(sub.Interests) != 1 || sub.Interests[0] != realtime.InterestChannelState || sub.Channel != (kernel.ID{1}) {
			t.Fatalf("pins scope: %+v, %v", sub, ok)
		}
	}
}
