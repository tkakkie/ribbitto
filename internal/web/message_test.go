package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type fakeMessages struct {
	channels *fakeChannels
	entries  []message.Entry
	older    bool
	err      error
	// before, when set, records the bound each read received.
	before *[]*int64
}

// One is not used by handler tests; the stream is tested end to end.
func (fakeMessages) Many(context.Context, authz.Membership, domain.ID, []domain.ID) ([]message.Entry, error) {
	return nil, message.ErrNotFound
}

func (fakeMessages) One(context.Context, authz.Membership, domain.ID, int64) (message.Entry, error) {
	return message.Entry{}, message.ErrNotFound
}

func (f fakeMessages) Page(ctx context.Context, m authz.Membership, id domain.ID, topicID *domain.ID, before *int64) (message.ChannelPage, error) {
	if f.before != nil {
		*f.before = append(*f.before, before)
	}
	channels := f.channels
	if channels == nil {
		channels = &fakeChannels{}
	}
	current, err := channels.Get(ctx, m, id)
	if err != nil {
		return message.ChannelPage{}, err
	}
	list, err := channels.List(ctx, m)
	if err != nil {
		return message.ChannelPage{}, err
	}
	page := message.ChannelPage{Page: message.Page{Entries: f.entries, Older: f.older}, Current: current, Channels: list}
	if topicID != nil {
		page.Topic = &domain.Topic{ID: *topicID, Name: "Planning"}
	}
	if before == nil && topicID == nil {
		cursor := int64(42)
		page.EventCursor = &cursor
	}
	return page, f.err
}

func populatedMessages() fakeMessages {
	return fakeMessages{entries: []message.Entry{
		{Message: domain.Message{ID: domain.ID{8}, ChannelID: domain.ID{1}, TopicID: domain.ID{0x31}, EventSeq: 7, Body: "<script>bad()</script>\nمرحبا\u2069", CreatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}, DisplayName: "مريم", Handle: "author", TopicName: "<design>مرحبا"},
		{Message: domain.Message{ID: domain.ID{9}, ChannelID: domain.ID{1}, TopicID: domain.ID{0x32}, EventSeq: 8, Body: "second", CreatedAt: time.Now()}, DisplayName: "\u3164", Handle: "legacy", DefaultTopic: true},
	}}
}

func olderMessages() fakeMessages {
	f := populatedMessages()
	f.older = true
	return f
}

func TestMessageListHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		reader fakeMessages
		status int
	}{
		{"populated", populatedMessages(), 200}, {"empty", fakeMessages{}, 200}, {"read failure", fakeMessages{err: errors.New("offline")}, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages = tt.reader
				s.Channels = &fakeChannels{getErr: errors.New("unexpected separate channel read"), listErr: errors.New("unexpected separate sidebar read")}
			}))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", view.ChannelURL("acme", domain.ID{1}), nil)
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if tt.status != 200 {
				return
			}
			body := w.Body.String()
			if strings.Contains(body, "data-announcement") {
				t.Fatal("history and Load older responses must not carry announcement text")
			}
			assertFullConversationPage(t, body)
			doc, err := html.Parse(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if outer := find(doc, atom.Div); attr(outer, "data-event-cursor") != "42" || attr(outer, "sse-connect") != view.ChannelURL("acme", domain.ID{1})+"/events?after=42" || attr(outer, "hx-ext") != "sse" || outer.Parent.DataAtom != atom.Body {
				t.Fatal("cursor must be on the outer layout, outside every swap target")
			}
			if items := find(doc, atom.Ol); attr(items, "sse-swap") != "message,messages-moved" {
				t.Fatal("latest message list must receive message events")
			}
			// An empty channel still loads it: messages swapped in later need it.
			if script := `src="/static/message-time-v2.js" nonce="` + responseNonce(t, w) + `"`; !strings.Contains(body, script) {
				t.Errorf("missing %q", script)
			}
			if len(tt.reader.entries) == 0 {
				return
			}
			for _, want := range []string{fmt.Sprintf(`id="message-%x"`, domain.ID{8}), `dir="auto"`, `whitespace-pre-wrap`, "&lt;script&gt;bad()&lt;/script&gt;\nمرحبا\u2069", `datetime="2026-09-29T12:00:00Z"`, "2026-09-29 12:00:00 UTC", `>مريم</bdi>`, "@author", "@legacy"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Contains(body, "\u3164") || strings.Contains(body, "<script>bad()") {
				t.Fatal("unsafe body or blank-looking name")
			}
		})
	}
}

func TestMessageTimestampView(t *testing.T) {
	for _, tt := range []struct {
		name     string
		created  time.Time
		datetime string
	}{
		{"whole second", time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), "2026-09-29T12:00:00Z"},
		{"microseconds", time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC), "2026-09-29T12:00:00.123Z"},
		{"no rounding", time.Date(2026, 9, 29, 12, 0, 0, 999999000, time.UTC), "2026-09-29T12:00:00.999Z"},
		{"offset to UTC", time.Date(2026, 9, 29, 21, 0, 0, 123456000, time.FixedZone("JST", 9*60*60)), "2026-09-29T12:00:00.123Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page := view.ChannelPage{
				Organization: domain.Organization{Name: "Acme", Slug: "acme"},
				Messages:     []view.Message{{CreatedAt: tt.created}},
			}
			var b strings.Builder
			if err := view.Channel("", page).Render(context.Background(), &b); err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(strings.NewReader(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			element := find(doc, atom.Time)
			if element == nil {
				t.Fatal("missing timestamp")
			}
			if got := attr(element, "datetime"); got != tt.datetime {
				t.Errorf("datetime = %q, want %q", got, tt.datetime)
			}
			if got := text(element); got != "2026-09-29 12:00:00 UTC" {
				t.Errorf("UTC fallback = %q", got)
			}
			if page.Messages[0].CreatedAt != tt.created {
				t.Error("rendering changed the message timestamp")
			}
		})
	}
}

type postingStore struct {
	id                   domain.ID
	err                  error
	body                 string
	org, channel, member domain.ID
}

func (s *postingStore) PostToTopic(_ context.Context, org, ch, member domain.ID, _ *domain.ID, body string) (domain.Message, error) {
	s.org, s.channel, s.member, s.body = org, ch, member, body
	return domain.Message{ID: s.id, Body: body}, s.err
}

func testPoster() *message.Service { return message.New(&postingStore{}) }

func TestMessagePostHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, body string
		storeErr   error
		status     int
	}{
		{"success", "  hello\r\nworld  ", nil, 303},
		{"empty", " \n ", nil, 422},
		{"leading newline", "\n\n", nil, 422},
		{"long", strings.Repeat("界", 4001), nil, 422},
		{"forbidden", "<script>\u202ebad</script>", nil, 422},
		{"missing", "hello", channel.ErrNotFound, 404},
		{"membership gone", "hello", authz.ErrNotFound, 404},
		{"failure", "hello", errors.New("offline"), 500},
	} {
		for _, hx := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/htmx=%t", tt.name, hx), func(t *testing.T) {
				store := &postingStore{id: domain.ID{37}, err: tt.storeErr}
				var reads []*int64
				h, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
					reader := populatedMessages()
					reader.before = &reads
					s.Messages, s.Posting = reader, message.New(store)
				}))
				if err != nil {
					t.Fatal(err)
				}
				path := view.ChannelURL("acme", domain.ID{1})
				r := httptest.NewRequest("POST", path+"?body=wrong", strings.NewReader(url.Values{"body": {tt.body}, "organization_id": {"other"}, "member_id": {"other"}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if hx {
					r.Header.Set("HX-Request", "true")
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := tt.status
				if hx && want == 303 {
					want = 200
				}
				if w.Code != want {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				if tt.status == 303 {
					if store.body != "hello\nworld" || store.org != (domain.ID{}) || store.member != (domain.ID{}) || store.channel != (domain.ID{1}) {
						t.Fatalf("posting scope/body: %+v", store)
					}
					if !hx && w.Header().Get("Location") != path {
						t.Fatal("wrong redirect")
					}
				}
				marker := `data-posted-message="` + view.MessageDOMID(store.id) + `"`
				if hx && want == 200 {
					if !strings.Contains(w.Body.String(), marker) {
						t.Fatal("success must identify the posted message for stream correlation")
					}
				} else if strings.Contains(w.Body.String(), "data-posted-message") {
					t.Fatal("only enhanced success may carry a posted message ID")
				}
				if want != 422 && want != 200 {
					return
				}
				if hx {
					if len(reads) != 0 || strings.Contains(w.Body.String(), `id="conversation"`) || !strings.HasPrefix(w.Body.String(), `<form id="message-composer"`) {
						t.Fatal("enhanced post must render only the composer without reading history")
					}
				} else {
					assertFullConversationPage(t, w.Body.String())
				}
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				field := find(doc, atom.Textarea)
				if want == 422 {
					if text(field) != tt.body || attr(field, "aria-invalid") != "true" || store.body != "" {
						t.Fatal("invalid input lost or posted")
					}
					checkFieldError(t, doc, field)
				} else if text(field) != "" {
					t.Fatal("composer not cleared")
				}
			})
		}
	}
}

func TestMessagePagingHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	channelURL := view.ChannelURL("acme", domain.ID{1})
	for _, tt := range []struct {
		name, query string
		older       bool
		status      int
		before      int64 // 0: the latest page
		want, avoid []string
		htmx        bool
	}{
		{"latest with older", "", true, 200, 0, []string{`href="` + channelURL + `?before=7"`, `hx-get="` + channelURL + `?before=7"`, `hx-select-oob="#load-older"`, `id="load-older"`}, []string{"Jump to the newest"}, false},
		{"latest without older", "", false, 200, 0, []string{`<div id="load-older" data-oldest-seq="0"></div>`}, []string{"?before=", "Jump to the newest"}, false},
		{"older page", "?before=40", true, 200, 40, []string{`?before=7"`, `>Jump to the newest messages</a>`}, nil, false},
		{"older page with HX", "?before=40", true, 200, 40, []string{`?before=7"`, `>Jump to the newest messages</a>`}, nil, true},
		{"oldest page", "?before=8", false, 200, 8, []string{`>Jump to the newest messages</a>`, `<div id="load-older" data-oldest-seq="0"></div>`}, []string{"?before="}, false},
		{"oldest page with HX", "?before=8", false, 200, 8, []string{`<div id="load-older" data-oldest-seq="0"></div>`}, []string{"?before="}, true},
		{"zero", "?before=0", false, 400, -1, nil, nil, false},
		{"negative", "?before=-3", false, 400, -1, nil, nil, false},
		{"not a number", "?before=abc", false, 400, -1, nil, nil, false},
		{"empty", "?before=", false, 400, -1, nil, nil, false},
		{"repeated", "?before=5&before=6", false, 400, -1, nil, nil, false},
		{"overflow", "?before=9223372036854775808", false, 400, -1, nil, nil, false},
		{"malformed escape", "?before=%ZZ", false, 400, -1, nil, nil, false},
		{"repeated with a malformed escape", "?before=5&before=%ZZ", false, 400, -1, nil, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var seen []*int64
			reader := fakeMessages{entries: []message.Entry{{Message: domain.Message{ID: domain.ID{8}, EventSeq: 7, Body: "oldest shown"}, DisplayName: "A", Handle: "a"}}, older: tt.older, before: &seen}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = reader }))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", channelURL+tt.query, nil)
			if tt.htmx {
				req.Header.Set("HX-Request", "true")
			}
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if tt.status != 200 {
				if len(seen) != 0 {
					t.Fatal("a malformed bound reached the reader")
				}
				return
			}
			if len(seen) != 1 || (tt.before == 0) != (seen[0] == nil) || (seen[0] != nil && *seen[0] != tt.before) {
				t.Fatalf("reader bounds %v", seen)
			}
			body := w.Body.String()
			if strings.Contains(body, "data-announcement") {
				t.Fatal("history and Load older responses must not carry announcement text")
			}
			assertFullConversationPage(t, body)
			if got := strings.Contains(body, `data-event-cursor="42"`); got != (tt.before == 0) {
				t.Fatalf("page cursor present = %t, before = %d", got, tt.before)
			}
			if tt.before != 0 && (strings.Contains(body, "data-event-cursor") || strings.Contains(body, "sse-connect") || strings.Contains(body, `hx-post="`+view.ChannelURL("acme", domain.ID{1})+`"`)) {
				t.Fatal("older page has a cursor, stream or enhanced composer")
			}
			if tt.before > 0 {
				doc, err := html.Parse(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				for n := range doc.Descendants() {
					if attr(n, "id") != "message-composer" {
						continue
					}
					posted := serveForm(handler, "POST", attr(n, "action"), "live", url.Values{"body": {"from older page"}})
					if posted.Code != 303 || posted.Header().Get("Location") != channelURL {
						t.Fatal("older-page form must redirect to the latest page")
					}
					invalid := serveForm(handler, "POST", attr(n, "action"), "live", url.Values{"body": {"\n\n"}})
					if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), "\n\n\n</textarea>") {
						t.Fatal("older-page form must retain an invalid draft")
					}
					assertFullConversationPage(t, invalid.Body.String())
				}
			}
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, avoid := range tt.avoid {
				if strings.Contains(body, avoid) {
					t.Errorf("unexpected %q", avoid)
				}
			}
		})
	}
}

func TestFeedTopicLabels(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = populatedMessages() }))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		for _, suffix := range []string{"", "?before=9"} {
			t.Run(lang+suffix, func(t *testing.T) {
				req := httptest.NewRequest("GET", view.ChannelURL("acme", domain.ID{1})+suffix, nil)
				req.Header.Set("Accept-Language", lang)
				req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				body := w.Body.String()
				if w.Code != 200 || !strings.Contains(body, `<bdi class="text-caption text-muted">&lt;design&gt;مرحبا</bdi>`) || !strings.Contains(body, `<bdi class="text-caption text-muted">chorus</bdi>`) {
					t.Fatalf("feed labels: %d, %s", w.Code, body)
				}
				// Each label links to its topic view (#303).
				for _, entry := range populatedMessages().entries {
					if link := labelLink(entry); !strings.Contains(body, link) {
						t.Fatalf("feed label link %s missing: %s", link, body)
					}
				}
				catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					for _, entry := range populatedMessages().entries {
						var live bytes.Buffer
						if err := view.LiveMessageItem(viewMessage("acme", entry)).Render(r.Context(), &live); err != nil {
							t.Fatal(err)
						}
						want := "&lt;design&gt;مرحبا"
						if entry.DefaultTopic {
							want = "chorus"
						}
						if !strings.Contains(live.String(), `<bdi class="text-caption text-muted">`+want+`</bdi>`) {
							t.Fatalf("live label: %s", live.String())
						}
						if link := labelLink(entry); !strings.Contains(live.String(), link) {
							t.Fatalf("live label link %s missing: %s", link, live.String())
						}
					}
				})).ServeHTTP(httptest.NewRecorder(), req)
			})
		}
	}
}

// labelLink is the markup of entry's topic label linking to its own topic
// view in organisation acme: each label must point at its message's topic.
func labelLink(entry message.Entry) string {
	label := "&lt;design&gt;مرحبا"
	if entry.DefaultTopic {
		label = "chorus"
	}
	return `<a href="` + view.ConversationURL("acme", entry.ChannelID, &entry.TopicID) + `" class="text-muted underline"><bdi class="text-caption text-muted">` + label + `</bdi></a>`
}
