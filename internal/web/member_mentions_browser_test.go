package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testMemberMentionsBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	viewer, other, unknown, topic := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}, kernel.ID{4}
	body := "<@" + realtime.FormatPayloadID(viewer) + "> <@" + realtime.FormatPayloadID(other) + "> <@" + realtime.FormatPayloadID(unknown) + ">"
	message := func(seq byte) view.Message {
		return viewMessage("acme", conversation.Entry{Message: conversation.Message{ID: kernel.ID{seq}, TopicID: topic, EventSeq: int64(seq)},
			Blocks: conversation.ParseChatV1(body), References: map[kernel.ID]conversation.Reference{viewer: {Handle: "alice"}, other: {Handle: "bob"}}})
	}
	snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Current: view.Channel{ID: kernel.ID{5}, Name: "general"},
		Role: "owner", MemberID: viewer, Handle: "alice", EventCursor: new(int64), Older: true, Messages: []view.Message{message(20)}}
	older := snapshot
	older.Before, older.EventCursor, older.Older, older.Messages = 20, nil, false, []view.Message{message(10)}
	mux := messagePageMux(t, snapshot, http.FileServerFS(static.FS()))
	mux.Handle("GET /organizations/acme/channels/{channel}", templ.Handler(view.ChannelScreen("/static/css/app.css", older)))
	server := httptest.NewServer(mux)
	defer server.Close()
	defer server.CloseClientConnections()
	page, closePage := boundedPage(t, browser, ctx, "member mentions")
	defer closePage()
	page = page.WithPanic(func(v any) { t.Fatal(v) })
	page.MustNavigate(server.URL + "/").MustWaitLoad()
	assert := func(seq byte) {
		t.Helper()
		selector := "#body-" + view.MessageDOMID(kernel.ID{seq}) + " > p > bdi"
		page.MustWait(`s => document.querySelectorAll(s).length === 3 && document.querySelector(s).hasAttribute('data-own-mention')`, selector)
		if got := page.MustEval(`s => [...document.querySelectorAll(s)].map(e => {
			const c = getComputedStyle(e);
			return [e.hasAttribute('data-member-id'), e.hasAttribute('data-own-mention'), c.outlineStyle, c.outlineWidth].join();
		}).join(';')`, selector).Str(); got != "true,true,solid,2px;true,false,none,0px;false,false,none,0px" {
			t.Fatalf("message %d: resolved ID, own highlight, outline = %s", seq, got)
		}
	}
	assert(20)
	page.MustEval(`() => document.querySelector('#load-older a').click()`)
	assert(10)
	// Exercise the production stream handler, including replay replacement.
	for range 2 {
		page.MustEval(`html => htmx.trigger(document.getElementById('message-items'), 'htmx:sseBeforeMessage', {
			type: 'message', data: html, lastEventId: '30'
		})`, string(streamTestMarkup(t, view.LiveMessageItem(message(30)))))
		assert(30)
	}
	moved := message(30)
	moved.TopicID = kernel.ID{6}
	page.MustEval(`html => htmx.trigger(document.getElementById('message-items'), 'htmx:sseBeforeMessage', {
		type: 'messages-moved', data: html, lastEventId: '31'
	})`, string(streamTestMarkup(t, view.MovedMessageItems([]view.Message{moved}, topic, moved.TopicID))))
	assert(30)
}
