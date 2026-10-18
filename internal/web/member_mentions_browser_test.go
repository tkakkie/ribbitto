package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testMemberMentionsBrowser(t *testing.T, browser *rod.Browser) {
	for _, mode := range []string{"feed", "topic"} {
		t.Run(mode, func(t *testing.T) { testMemberMentionsViewBrowser(t, browser, mode == "topic") })
	}
}

func testMemberMentionsViewBrowser(t *testing.T, browser *rod.Browser, topicView bool) {
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
	if topicView {
		snapshot.Topic = &view.Topic{ID: topic}
	}
	older := snapshot
	older.Before, older.EventCursor, older.Older, older.Messages = 20, nil, false, []view.Message{message(10)}
	markup := func(component templ.Component) []byte {
		t.Helper()
		data := streamTestMarkup(t, component)
		if bytes.Contains(data, []byte("data-own-mention=")) {
			t.Fatal("the server rendered a reader-dependent highlight")
		}
		return data
	}
	initialHTML := markup(view.ChannelScreen("/static/css/app.css", snapshot))
	olderHTML := markup(view.ChannelScreen("/static/css/app.css", older))
	events := make(chan realtime.Outgoing)
	mux := memberMentionsMux(t, initialHTML, olderHTML, events)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer server.CloseClientConnections()
	page, closePage := boundedPage(t, browser, ctx, "member mentions")
	defer closePage()
	page = page.WithPanic(func(v any) { t.Fatal(v) })
	setScheme := func(scheme string) {
		t.Helper()
		if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: scheme}}}).Call(page); err != nil {
			t.Fatal(err)
		}
		page.MustWait(`scheme => matchMedia('(prefers-color-scheme: ' + scheme + ')').matches`, scheme)
	}
	setScheme("light")
	page.MustEvalOnNewDocument(`window.mentionBefore = null;
		document.addEventListener('htmx:sseOpen', e => {
			if (e.target.id === 'organization-stream') window.mentionsStreamOpen = true;
		})`)
	page.MustNavigate(server.URL + "/").MustWaitLoad()
	page.MustWait(`() => window.mentionsStreamOpen === true`)
	remember := func(seq byte, present bool) {
		t.Helper()
		id := view.MessageDOMID(kernel.ID{seq})
		page.MustWait(`(id, present) => !!document.getElementById(id) === present`, id, present)
		// A missing node is intentional for inserts; replacements must first
		// capture the existing node so an old highlight cannot satisfy assert.
		page.MustEval(`id => { window.mentionBefore = document.getElementById(id); }`, id)
	}
	assert := func(seq byte) {
		t.Helper()
		selector := "#body-" + view.MessageDOMID(kernel.ID{seq}) + " > p > bdi"
		// Payloads have no highlight attribute. A fresh node must acquire it,
		// including replay/move replacements of an already highlighted item.
		page.MustWait(`s => document.querySelectorAll(s).length === 3 &&
			document.querySelector(s).closest('li') !== window.mentionBefore &&
			document.querySelector(s).hasAttribute('data-own-mention')`, selector)
		defer setScheme("light")
		for _, scheme := range []string{"light", "dark"} {
			setScheme(scheme)
			if got := page.MustEval(mentionOutlineStyles, selector).Str(); got != "true,true,dashed,2px,0px,true;true,false,none;false,false,none" {
				t.Fatalf("message %d (%s): resolved ID, own highlight, outline, contrast = %s", seq, scheme, got)
			}
		}
	}
	// No history request or SSE message has run: only htmx's initial body
	// load can highlight this server-rendered message.
	assert(20)
	remember(10, false)
	page.MustEval(`() => document.querySelector('#load-older a').click()`)
	assert(10)
	send := func(out realtime.Outgoing) {
		t.Helper()
		select {
		case events <- out:
		case <-ctx.Done():
			t.Fatal("stream did not receive frame", ctx.Err())
		}
	}
	// The real EventSource and SSE extension deliver new posts and replays to
	// message-stream's production handler; no synthetic load events are sent.
	for delivery := range 2 {
		remember(30, delivery > 0)
		send(realtime.Outgoing{ID: 30, Name: "message", Data: markup(view.LiveMessageItem(message(30)))})
		assert(30)
	}
	moved := message(30)
	moved.TopicID = kernel.ID{6}
	remember(30, true)
	send(realtime.Outgoing{ID: 31, Name: "messages-moved", Data: markup(view.MovedMessageItems([]view.Message{moved}, topic, moved.TopicID))})
	if topicView {
		page.MustWait(`id => !document.getElementById(id)`, view.MessageDOMID(moved.ID))
		// Moving back exercises destination insertion, not feed replacement.
		moved.TopicID = topic
		remember(30, false)
		send(realtime.Outgoing{ID: 32, Name: "messages-moved", Data: markup(view.MovedMessageItems([]view.Message{moved}, kernel.ID{6}, topic))})
	}
	assert(30)
}

// memberMentionsMux keeps the page, history and real SSE transport together
// so its route registrations can also be exercised without launching Chrome.
func memberMentionsMux(t *testing.T, initialHTML, olderHTML []byte, events <-chan realtime.Outgoing) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("POST /organizations/acme/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := sender.Heartbeat(r.Context()); err != nil {
			return
		}
		for {
			select {
			case out := <-events:
				if err := sender.Send(r.Context(), out); err != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if before := r.URL.Query().Get("before"); before != "" {
			if before != "20" || r.Header.Get("HX-Request") != "true" {
				t.Errorf("unexpected history request: before=%s, HX-Request=%s", before, r.Header.Get("HX-Request"))
			}
			_, _ = w.Write(olderHTML)
			return
		}
		_, _ = w.Write(initialHTML)
	})
	return mux
}

// Width is meaningful only for a visible outline: Chrome reports medium
// (3px) for outline-style:none. Check the shape and non-text contrast against
// both the chip and its nearest painted ancestor in each emulated theme.
const mentionOutlineStyles = `s => {
	const luminance = color => {
		const rgb = color.match(/[\d.]+/g).slice(0, 3).map(v => {
			const c = +v / 255;
			return c <= .04045 ? c / 12.92 : ((c + .055) / 1.055) ** 2.4;
		});
		return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722;
	};
	const contrast = (a, b) => (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
	return [...document.querySelectorAll(s)].map(e => {
		const c = getComputedStyle(e);
		const result = [e.hasAttribute('data-member-id'), e.hasAttribute('data-own-mention'), c.outlineStyle];
		if (c.outlineStyle !== 'none') {
			let parent = e.parentElement;
			while (parent && getComputedStyle(parent).backgroundColor === 'rgba(0, 0, 0, 0)') parent = parent.parentElement;
			if (!parent) throw new Error('mention has no painted ancestor');
			const outline = luminance(c.outlineColor);
			result.push(c.outlineWidth, c.outlineOffset, contrast(outline, luminance(c.backgroundColor)) >= 3 &&
				contrast(outline, luminance(getComputedStyle(parent).backgroundColor)) >= 3);
		}
		return result.join();
	}).join(';');
}`
