package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// pageNonce matches what differs on every full page: the script nonce and
// the render time.
var pageNonce = regexp.MustCompile(` (nonce|data-rendered-at)="[^"]*"`)

// Every composer path stores chat_v1; a reference to an unknown and to
// another organisation's member gets the same 422 and keeps the draft; a
// branch notice escapes its topic name.
func TestChatV1PostingAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "planning")
	bob := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob Example"), org.RoleMember, "bob", 1)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	spy := orgtest.Member(t, pool, foreign.OrganizationID, identitytest.Account(t, pool, "spy@example.org", "Foreign Spy"), org.RoleMember, "spy", 1)
	ref := func(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	owner, _, err := sessions.Create(ctx, f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler("", catalogues, postgresServices(t, pool, sessions, "", false))
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, form url.Values, hx bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: owner})
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	latest := func() (id kernel.ID, body, format string) {
		t.Helper()
		if err := pool.QueryRow(ctx, "SELECT id, body, format FROM message WHERE organization_id = $1 ORDER BY event_seq DESC LIMIT 1", f.OrganizationID).Scan(&id, &body, &format); err != nil {
			t.Fatal(err)
		}
		return id, body, format
	}
	feed := view.ConversationURL("acme", f.Channel.ID, nil)

	// The feed and topic composers post with htmx, an older page with a
	// plain form; each stores chat_v1, a member of the organisation as a
	// reference and a malformed token as typed.
	for _, tc := range []struct {
		name, path, body string
		hx               bool
		status           int
	}{
		{"feed", feed, "**hi** " + ref(bob), true, http.StatusOK},
		{"topic", view.ConversationURL("acme", f.Channel.ID, &topic.ID), "_hi_ " + ref(bob), true, http.StatusOK},
		{"older page", feed, "<@NOT-A-UUID> " + strings.ToUpper(ref(spy)), false, http.StatusSeeOther},
	} {
		if w := post(tc.path, url.Values{"body": {tc.body}}, tc.hx); w.Code != tc.status {
			t.Fatalf("%s: status %d: %s", tc.name, w.Code, w.Body.String())
		}
		if _, body, format := latest(); body != tc.body || format != string(conversation.FormatChatV1) {
			t.Fatalf("%s stored %q as %q", tc.name, body, format)
		}
	}

	// An unknown and another organisation's member: the same status and
	// message, the same answer once the echoed draft is normalised, and
	// nothing about the foreign member; no message is written.
	before, _, _ := latest()
	for _, hx := range []bool{true, false} {
		answer := func(member kernel.ID) string {
			t.Helper()
			draft := "hello " + ref(member)
			form := url.Values{"body": {draft}, "composer": {"submitted"}, "idempotency_key": {"01234567-89ab-4cde-8f01-23456789abcd"}, "cursor": {"1"}}
			w := post(feed, form, hx)
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "A member reference does not match a member of this organization.") {
				t.Fatalf("htmx=%t: status %d: %s", hx, w.Code, w.Body.String())
			}
			// An enhanced 422 leaves the draft in the page's textarea (#1173);
			// the plain form renders it again.
			if _, values := composerForm(t, w); !hx && values.Get("body") != draft {
				t.Fatalf("draft not kept: %q", values.Get("body"))
			}
			// A full page's script nonce is fresh on every response.
			return pageNonce.ReplaceAllString(strings.ReplaceAll(w.Body.String(), realtime.FormatPayloadID(member), "MEMBER"), "")
		}
		unknown, other := answer(kernel.ID{0xee}), answer(spy)
		if unknown != other {
			t.Errorf("htmx=%t: the answers differ:\n%s\n%s", hx, unknown, other)
		}
		if strings.Contains(other, "Foreign Spy") || strings.Contains(other, "@spy") {
			t.Errorf("htmx=%t: the answer resolves the foreign member", hx)
		}
	}
	if after, _, _ := latest(); after != before {
		t.Fatal("a refused reference was posted")
	}

	// A branch notice is escaped chat_v1: a topic named like markup or a
	// foreign reference shows as typed and resolves no one.
	for _, name := range []string{"**Deploy**", ref(spy)} {
		message, _, _ := latest()
		w := post(view.ChannelURL("acme", f.Channel.ID)+"/branch", url.Values{"message": {realtime.FormatPayloadID(message)}, "from": {realtime.FormatPayloadID(f.Channel.DefaultTopicID)}, "name": {name}}, false)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("branch to %q: status %d: %s", name, w.Code, w.Body.String())
		}
		notice, body, format := latest()
		if format != string(conversation.FormatChatV1) || body != conversation.EscapeChatV1(fmt.Sprintf("Moved 1 to “%s”", name)) {
			t.Fatalf("notice %q as %q", body, format)
		}
		page := serveForm(h, http.MethodGet, feed, owner, nil).Body.String()
		want := `<p dir="auto" class="whitespace-pre-wrap">Moved 1 to “` + strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(name) + `”</p>`
		if !strings.Contains(page, `id="body-`+view.MessageDOMID(notice)+`"`) || !strings.Contains(page, want) {
			t.Errorf("notice for %q is not shown as typed", name)
		}
		if strings.Contains(page, "Foreign Spy") || strings.Contains(page, "@spy") || strings.Contains(page, "@unknown member") {
			t.Errorf("notice for %q resolved a reference", name)
		}
	}
}
