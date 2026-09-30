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
	entries []message.Entry
	older   bool
	err     error
	// before, when set, records the bound each read received.
	before *[]*int64
}

func (f fakeMessages) Before(_ context.Context, _ authz.Membership, _ domain.ID, before *int64) (message.Page, error) {
	if f.before != nil {
		*f.before = append(*f.before, before)
	}
	return message.Page{Entries: f.entries, Older: f.older}, f.err
}

func populatedMessages() fakeMessages {
	return fakeMessages{entries: []message.Entry{
		{Message: domain.Message{ID: domain.ID{8}, Body: "<script>bad()</script>\nمرحبا\u2069", CreatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}, DisplayName: "مريم", Handle: "author"},
		{Message: domain.Message{ID: domain.ID{9}, Body: "second", CreatedAt: time.Now()}, DisplayName: "\u3164", Handle: "legacy"},
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
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = tt.reader }))
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
				Messages:     []message.Entry{{Message: domain.Message{CreatedAt: tt.created}}},
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
	err                  error
	body                 string
	org, channel, member domain.ID
}

func (s *postingStore) Post(_ context.Context, org, ch, member domain.ID, body string) (domain.Message, error) {
	s.org, s.channel, s.member, s.body = org, ch, member, body
	return domain.Message{Body: body}, s.err
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
				store := &postingStore{err: tt.storeErr}
				h, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Posting = populatedMessages(), message.New(store) }))
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
				if want != 422 && want != 200 {
					return
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
					var alert *html.Node
					for n := range doc.Descendants() {
						if attr(n, "role") == "alert" {
							alert = n
						}
					}
					if alert == nil || attr(field, "aria-describedby") != attr(alert, "id") {
						t.Fatal("missing associated field error")
					}
				} else if text(field) != "" || !strings.Contains(text(doc), "second") {
					t.Fatal("composer not cleared or history missing")
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
	}{
		{"latest with older", "", true, 200, 0, []string{`href="` + channelURL + `?before=7"`, `hx-get="` + channelURL + `?before=7"`, `hx-select-oob="#load-older"`, `id="load-older"`}, []string{"Jump to the newest"}},
		{"latest without older", "", false, 200, 0, []string{`<div id="load-older"></div>`}, []string{"?before=", "Jump to the newest"}},
		{"older page", "?before=40", true, 200, 40, []string{`?before=7"`, `>Jump to the newest messages</a>`}, nil},
		{"oldest page", "?before=8", false, 200, 8, []string{`>Jump to the newest messages</a>`}, []string{"?before="}},
		{"zero", "?before=0", false, 400, -1, nil, nil},
		{"negative", "?before=-3", false, 400, -1, nil, nil},
		{"not a number", "?before=abc", false, 400, -1, nil, nil},
		{"empty", "?before=", false, 400, -1, nil, nil},
		{"repeated", "?before=5&before=6", false, 400, -1, nil, nil},
		{"overflow", "?before=9223372036854775808", false, 400, -1, nil, nil},
		{"malformed escape", "?before=%ZZ", false, 400, -1, nil, nil},
		{"repeated with a malformed escape", "?before=5&before=%ZZ", false, 400, -1, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var seen []*int64
			reader := fakeMessages{entries: []message.Entry{{Message: domain.Message{ID: domain.ID{8}, EventSeq: 7, Body: "oldest shown"}, DisplayName: "A", Handle: "a"}}, older: tt.older, before: &seen}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = reader }))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", channelURL+tt.query, nil)
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
