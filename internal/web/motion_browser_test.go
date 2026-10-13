package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Only live appends move (#878), reconnect backlog included: the initial
// page, a replay replacing a shown message, a move insertion and a Load
// older prepend stay still. Reduced motion stops every motion on the page.
func testMessageMotionBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cursor, source, topic := int64(42), kernel.ID{1}, kernel.ID{2}
	snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current: view.Channel{ID: kernel.ID{1}}, Topic: &view.Topic{ID: topic}, EventCursor: &cursor,
		Older: true, Messages: streamTestMessages(topic, 40, 60)}
	streams := make(chan string, 2)
	frames := make(chan realtime.Outgoing)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/acme/events" {
			page := snapshot
			if r.URL.Query().Get("before") != "" {
				page.Before, page.EventCursor, page.Older = 40, nil, false
				page.Messages = streamTestMessages(topic, 20)
			}
			templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
			return
		}
		streams <- r.URL.Query().Get("after")
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		for err := sender.Heartbeat(r.Context()); err == nil; {
			select {
			case out := <-frames:
				err = sender.Send(r.Context(), out)
			case <-r.Context().Done():
				return
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	motion := func(value string) {
		if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-reduced-motion", Value: value}}}).Call(page); err != nil {
			t.Fatal(err)
		}
	}
	motion("no-preference")
	page.MustEvalOnNewDocument(`document.addEventListener('htmx:sseOpen', e => window.testSource = e.detail.source)`)
	page.MustNavigate(server.URL).MustWaitLoad()
	connected := func(want string) {
		t.Helper()
		select {
		case after := <-streams:
			if after != want {
				t.Fatalf("stream after=%s, want %s", after, want)
			}
		case <-ctx.Done():
			t.Fatal("stream did not connect", ctx.Err())
		}
		page.MustWait(`() => window.testSource?.readyState === EventSource.OPEN`)
	}
	send := func(out realtime.Outgoing) {
		t.Helper()
		select {
		case frames <- out:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	message := func(id int64, seq int) realtime.Outgoing {
		return realtime.Outgoing{ID: id, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, seq)[0]))}
	}
	// The class records what the script decided; the computed animation
	// shows whether the stylesheet lets it move.
	assertMotion := func(seq int, class bool, animation string) {
		t.Helper()
		page.MustWait(`id => !!document.getElementById(id) && !document.querySelector('.htmx-settling')`, view.MessageDOMID(kernel.ID{byte(seq)}))
		got := page.MustEval(`id => { const item = document.getElementById(id);
			return item.classList.contains('animate-message-in') + ' ' + getComputedStyle(item).animationName; }`,
			view.MessageDOMID(kernel.ID{byte(seq)})).Str()
		if want := map[bool]string{true: "true ", false: "false "}[class] + animation; got != want {
			t.Fatalf("message %d: class and animation %q, want %q", seq, got, want)
		}
	}
	connected("42")
	assertMotion(40, false, "none")
	assertMotion(60, false, "none")
	send(message(61, 61))
	assertMotion(61, true, "message-in")
	// A replay replaces the animated item with one that stays still.
	page.MustEval(`id => window.replaced = document.getElementById(id)`, view.MessageDOMID(kernel.ID{61}))
	send(message(62, 61))
	page.MustWait(`id => document.getElementById(id) !== window.replaced`, view.MessageDOMID(kernel.ID{61}))
	assertMotion(61, false, "none")
	send(realtime.Outgoing{ID: 63, Name: "messages-moved",
		Data: streamTestMarkup(t, view.MovedMessageItems(streamTestMessages(topic, 50), source, topic))})
	assertMotion(50, false, "none")
	page.MustEval(`() => document.querySelector('#load-older a').click()`)
	assertMotion(20, false, "none")
	assertBrowserItems(t, page, []int{20, 40, 50, 60, 61})
	// Backlog after a reconnect is new to this page, so it moves too.
	page.MustEval(`() => { window.testSource.close(); window.testSource.dispatchEvent(new Event('error')); }`)
	connected("63")
	send(message(64, 64))
	assertMotion(64, true, "message-in")
	typists := []typing.Typist{{DisplayName: "Alice", Handle: "alice"}}
	send(realtime.Outgoing{Ephemeral: true, Name: "typing", Data: streamTestMarkup(t, view.TypingIndicator(typing.Snapshot{Typists: typists}))})
	page.MustWait(`() => !!document.querySelector('#typing-status .animate-typing-dot-1')`)
	states := `() => [getComputedStyle(document.querySelector('#typing-status .animate-typing-dot-1')).animationName,
		getComputedStyle(document.querySelector('[popovertarget=account-menu]')).transitionDuration].join(' ')`
	if got := page.MustEval(states).Str(); got != "typing-dot 0.1s" {
		t.Fatalf("typing dot and button feedback = %q, want typing-dot 0.1s", got)
	}
	motion("reduce")
	send(message(65, 65))
	assertMotion(65, true, "none")
	if got := page.MustEval(states).Str(); got != "none 0s" {
		t.Fatalf("reduced motion: typing dot and button feedback = %q, want none 0s", got)
	}
}
