package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestTopicPostScopeAgainstPostgreSQL proves that the enhanced topic post
// keeps the plain post's 404s with real scope lookups: neither a failed nor
// a valid post to an unknown topic, another channel's topic or as a
// non-member renders a composer, issues a generation or writes a message.
func TestTopicPostScopeAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "planning")
	elsewhere := conversationtest.Topic(t, pool, f.OrganizationID, other.ID, "elsewhere")
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	owner, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := sessions.Create(t.Context(), identitytest.Account(t, pool, "outsider@example.org", "outsider"))
	if err != nil {
		t.Fatal(err)
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	services := postgresServices(t, pool, sessions, "", false)
	services.Typing, services.SignalLimits = typing.New(realtime.NewHub()), middleware.NewMemberRateLimiter(time.Now)
	h, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	messages := func() (n int) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE organization_id = $1", f.OrganizationID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	post := func(cookie, path, body string, hx bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"body": {body}, "cursor": {"1"}, "composer": {"submitted"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	visible := view.ConversationURL("acme", f.Channel.ID, &topic.ID)
	for _, tc := range []struct{ name, cookie, path string }{
		{"unknown topic", owner, view.ConversationURL("acme", f.Channel.ID, &kernel.ID{0xfe})},
		{"other channel's topic", owner, view.ConversationURL("acme", f.Channel.ID, &elsewhere.ID)},
		{"non-member", foreign, visible},
	} {
		for _, hx := range []bool{false, true} {
			for _, body := range []string{"", "hello"} {
				t.Run(fmt.Sprintf("%s/htmx=%t/body=%q", tc.name, hx, body), func(t *testing.T) {
					before := messages()
					w := post(tc.cookie, tc.path, body, hx)
					if w.Code != http.StatusNotFound {
						t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
					}
					if strings.Contains(w.Body.String(), "message-composer") || strings.Contains(w.Body.String(), `name="composer"`) || messages() != before {
						t.Fatal("a refused topic post rendered a composer, issued a generation or wrote")
					}
				})
			}
		}
	}
	// The visible topic, by contrast, answers the enhanced fragments.
	if w := post(owner, visible, "", true); w.Code != http.StatusUnprocessableEntity || !strings.HasPrefix(w.Body.String(), `<form id="message-composer"`) || !strings.Contains(w.Body.String(), `name="composer" value="submitted"`) {
		t.Fatalf("visible topic 422: status %d: %s", w.Code, w.Body.String())
	}
	before := messages()
	if w := post(owner, visible, "hello", true); w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), `<form id="message-composer"`) || messages() != before+1 {
		t.Fatalf("visible topic post: status %d: %s", w.Code, w.Body.String())
	}
	var stored kernel.ID
	if err := pool.QueryRow(t.Context(), "SELECT topic_id FROM message WHERE organization_id = $1 ORDER BY event_seq DESC LIMIT 1", f.OrganizationID).Scan(&stored); err != nil || stored != topic.ID {
		t.Fatalf("posted into %v (%v), want the topic", stored, err)
	}
}
