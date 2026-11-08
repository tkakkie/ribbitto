package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestNativeReplyAgainstPostgreSQL(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "Planning")
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	posting := fixturePoster(pool)
	original, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Topic: &topic.ID, Body: "**Original**"})
	if err != nil {
		t.Fatal(err)
	}
	outside, err := posting.Post(t.Context(), m, other.ID, "secret")
	if err != nil {
		t.Fatal(err)
	}
	foreignPost, err := posting.Post(t.Context(), org.Membership{Organization: org.Organization{ID: foreign.OrganizationID}, Member: org.Member{ID: foreign.MemberID}}, foreign.Channel.ID, "foreign secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	services := postgresServices(t, pool, sessions, "", false)
	services.Typing = typing.New(realtime.NewHub())
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
	id := realtime.FormatPayloadID(original.Message.ID)
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range []struct {
			values []string
			status int
		}{
			{[]string{"bad"}, 400}, {[]string{strings.ToUpper(id)}, 400}, {[]string{id, id}, 400},
			{[]string{realtime.FormatPayloadID(kernel.ID{99})}, 404},
			{[]string{realtime.FormatPayloadID(outside.ID)}, 404}, {[]string{realtime.FormatPayloadID(foreignPost.ID)}, 404},
		} {
			form := url.Values{"reply": tc.values, "body": {"answer"}}
			path := feed
			if method == "GET" {
				path += "?" + form.Encode()
			}
			w := serveForm(handler, method, path, cookie, form)
			if w.Code != tc.status {
				t.Fatalf("%s %v: %d %s", method, tc.values, w.Code, w.Body.String())
			}
			if tc.status == 404 && w.Body.String() != "404 page not found\n" {
				t.Fatal("scope refusal disclosed content")
			}
		}
	}
	outsider, _, err := sessions.Create(t.Context(), identitytest.Account(t, pool, "outsider@example.org", "Outsider"))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		path := feed
		if method == "GET" {
			path += "?reply=" + id
		}
		if w := serveForm(handler, method, path, outsider, url.Values{"body": {"answer"}, "reply": {id}}); w.Code != 404 || w.Body.String() != "404 page not found\n" {
			t.Fatal("non-member could see or answer the target")
		}
	}
	w := httptest.NewRecorder()
	if (channelPages{messages: services.Messages}).readReply(w, httptest.NewRequest("GET", "/", nil), m, foreign.Channel.ID, url.Values{"reply": {realtime.FormatPayloadID(foreignPost.ID)}}, &view.ChannelPage{}) || w.Code != 404 {
		t.Fatal("reply reader crossed organisation scope")
	}
	// No session means the target is unreadable, even in the current channel.
	if w := serveForm(handler, "GET", feed+"?reply="+id, "", nil); w.Code != 404 {
		t.Fatalf("unreadable: %d", w.Code)
	}
	page := serveForm(handler, "GET", feed+"?reply="+id, cookie, nil)
	_, selected := composerForm(t, page)
	if page.Code != 200 || selected.Get("reply") != id || !strings.Contains(replyTargetMarkup(t, page.Body.String()), "Original") {
		t.Fatal("GET lost the target")
	}
	actions := serveForm(handler, "GET", view.MessageURL("acme", original.Message.ID)+"/actions", cookie, nil)
	if actions.Code != 200 || !strings.Contains(actions.Body.String(), `type="submit" name="reply" value="`+id+`"`) {
		t.Fatal("native actions lack Reply")
	}
	key := *freshKey()
	cancel := url.Values{"body": {"Draft answer"}, "reply": {id}, "quote": {id}, "clear_reply": {"1"}, "idempotency_key": {key}, "composer": {selected.Get("composer")}}
	w = serveForm(handler, "POST", feed, cookie, cancel)
	_, cancelledForm := composerForm(t, w)
	target := replyTargetMarkup(t, w.Body.String())
	if w.Code != 200 || cancelledForm.Has("reply") || cancelledForm.Get("body") != "Draft answer" || !strings.Contains(target, `<input type="hidden" name="quote" value="`+id+`"`) || strings.Contains(target, `id="composer-quote" hidden`) || cancelledForm.Get("idempotency_key") != key {
		t.Fatal("cancel lost the draft, quote or key, or retained reply")
	}
	if cancelledForm.Get("composer") == "" || cancelledForm.Get("composer") != selected.Get("composer") {
		t.Fatal("cancel lost composer generation")
	}
	var cancelled int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE idempotency_key = $1", key).Scan(&cancelled); err != nil || cancelled != 0 {
		t.Fatalf("cancel posted %d messages: %v", cancelled, err)
	}
	for _, place := range []*kernel.ID{nil, &f.Channel.DefaultTopicID} {
		path := view.ConversationURL("acme", f.Channel.ID, place)
		key := *freshKey()
		form := url.Values{"reply": {id}, "body": {"answer"}, "idempotency_key": {key}, "composer": {selected.Get("composer")}}
		if w := serveForm(handler, "POST", path, cookie, form); w.Code != 303 {
			t.Fatalf("native reply: %d %s", w.Code, w.Body.String())
		} else {
			next := serveForm(handler, "GET", w.Header().Get("Location"), cookie, nil)
			_, cleared := composerForm(t, next)
			if next.Code != 200 || cleared.Has("reply") || strings.TrimSpace(cleared.Get("body")) != "" {
				t.Fatal("successful native reply retained text or target")
			}
		}
		var storedTopic, target kernel.ID
		if err := pool.QueryRow(t.Context(), "SELECT topic_id, reply_to_message_id FROM message WHERE idempotency_key = $1", key).Scan(&storedTopic, &target); err != nil {
			t.Fatal(err)
		}
		want := topic.ID
		if place != nil {
			want = *place
		}
		if storedTopic != want || target != original.Message.ID {
			t.Fatal("reply topic/target changed")
		}
		// Validation preserves the submitted generation, just as a quoted send does.
		initial := serveForm(handler, "GET", path+"?reply="+id, cookie, nil)
		_, issued := composerForm(t, initial)
		form.Set("composer", issued.Get("composer"))
		form.Set("body", "")
		w := serveForm(handler, "POST", path, cookie, form)
		_, invalid := composerForm(t, w)
		if w.Code != 422 || invalid.Get("idempotency_key") != key || invalid.Get("reply") != id {
			t.Fatal("validation lost target or key")
		}
		if invalid.Get("composer") == "" || invalid.Get("composer") != issued.Get("composer") {
			t.Fatal("validation lost composer generation")
		}
		form.Set("body", "answer")
		if w := serveForm(handler, "POST", path, cookie, form); w.Code != 303 {
			t.Fatal("same-key retry failed")
		}
		form.Set("reply", realtime.FormatPayloadID(second.Message.ID))
		if w := serveForm(handler, "POST", path, cookie, form); w.Code != 409 {
			t.Fatal("changed target posted another message")
		}
		parsed, ok := pathID(key)
		if !ok {
			t.Fatal("invalid fixture key")
		}
		_, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Topic: place, Body: "answer", Key: new(conversation.IdempotencyKey(parsed)), ReplyTo: &second.Message.ID})
		if !errors.Is(err, conversation.ErrPostKeyReused) {
			t.Fatalf("changed target error: %v", err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE idempotency_key = $1", key).Scan(&count); err != nil || count != 1 {
			t.Fatalf("key stored %d messages: %v", count, err)
		}
	}
}

func replyTargetMarkup(t *testing.T, markup string) string {
	t.Helper()
	for n := range measureFeed(t, markup).doc.Descendants() {
		if attr(n, "id") == view.ComposerQuoteID {
			var output bytes.Buffer
			if err := html.Render(&output, n); err != nil {
				t.Fatal(err)
			}
			return output.String()
		}
	}
	t.Fatal("missing composer target container")
	return ""
}
