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
	err     error
}

func (f fakeMessages) Latest(context.Context, authz.Membership, domain.ID) ([]message.Entry, error) {
	return f.entries, f.err
}

func populatedMessages() fakeMessages {
	return fakeMessages{entries: []message.Entry{
		{Message: domain.Message{ID: domain.ID{8}, Body: "<script>bad()</script>\nمرحبا\u2069", CreatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}, DisplayName: "مريم", Handle: "author"},
		{Message: domain.Message{ID: domain.ID{9}, Body: "second", CreatedAt: time.Now()}, DisplayName: "\u3164", Handle: "legacy"},
	}}
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
			handler, err := NewHandler("", catalogues, Services{Sessions: oneSession{}, SignIn: &fakeSignIn{}, Authz: oneOrganisation{}, Channels: &fakeChannels{}, Posting: testPoster(), Messages: tt.reader})
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
			if script := `src="/static/message-time-v1.js" nonce="` + responseNonce(t, w) + `"`; !strings.Contains(body, script) {
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
				h, err := NewHandler("", catalogues, Services{Sessions: oneSession{}, SignIn: &fakeSignIn{}, Authz: oneOrganisation{}, Channels: &fakeChannels{}, Messages: populatedMessages(), Posting: message.New(store)})
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
