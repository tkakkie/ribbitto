package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestReplyPreviewPageAndStream(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "Planning")
	posting := fixturePoster(pool)
	original, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Topic: &topic.ID, Body: "**Original**"})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "answer", ReplyTo: &original.Message.ID})
	if err != nil {
		t.Fatal(err)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	services := postgresServices(t, pool, sessions, "", false)
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	feed := view.ChannelURL("acme", f.Channel.ID)

	entry, err := services.Messages.One(t.Context(), m, f.Channel.ID, sent.Message.EventSeq)
	if err != nil {
		t.Fatal(err)
	}
	expected := replyPreviewHTML(t, renderIn(t, "en", view.MessageItem(viewMessage(t.Context(), "acme", entry))))
	if !strings.Contains(expected, `href="`+view.MessageURL("acme", original.Message.ID)+`"`) || !strings.Contains(expected, `aria-label="In reply to `) || !strings.Contains(expected, ": Original") {
		t.Fatalf("preview lacks permalink or accessible name: %s", expected)
	}
	for _, query := range []string{"", "?before=999", "?around=" + strconv.FormatInt(entry.EventSeq, 10)} {
		page := serveForm(handler, "GET", feed+query, cookie, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("page %s: %d", query, page.Code)
		}
		if got := replyPreviewHTML(t, page.Body.String()); got != expected {
			t.Fatalf("page %s preview differs: %s != %s", query, got, expected)
		}
	}
	event := eventOf(t, f.OrganizationID, entry.EventSeq, conversation.KindPosted, conversation.Moved{ChannelID: f.Channel.ID, ToTopicID: entry.TopicID})
	frame, err := (messageRenderer{messages: services.Messages, membership: m, renders: newRenderCache(t.Context())}).Render(englishContext(t, catalogues), event)
	if err != nil || replyPreviewHTML(t, string(frame.Data)) != expected {
		t.Fatalf("stream preview differs: %v", err)
	}
}

func replyPreviewHTML(t *testing.T, markup string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		if n.Data == "a" && strings.HasPrefix(attr(n, "aria-label"), "In reply to ") {
			var out bytes.Buffer
			if err := html.Render(&out, n); err != nil {
				t.Fatal(err)
			}
			return out.String()
		}
	}
	t.Fatal("missing reply preview")
	return ""
}

func englishContext(t *testing.T, catalogues *i18n.Catalogues) context.Context {
	t.Helper()
	var ctx context.Context
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en")
	catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	return ctx
}
