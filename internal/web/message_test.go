package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
			handler, err := NewHandler("", catalogues, Services{Sessions: oneSession{}, SignIn: &fakeSignIn{}, Authz: oneOrganisation{}, Channels: &fakeChannels{}, Messages: tt.reader})
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
