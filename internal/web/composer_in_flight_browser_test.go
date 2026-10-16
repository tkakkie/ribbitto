package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// heldComposer serves the feed with every post held until the test releases
// it, so the test can type while a send is in flight (#1173).
func heldComposer(t *testing.T, browser *rod.Browser) (*rod.Page, func() chan struct{}, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	handler, _, _, place := composerHandler(t, false)
	var posts atomic.Int32
	held := make(chan chan struct{}, 4)
	// Ending the test releases every held post, so that a failure cannot
	// leave a handler waiting and server.Close blocked.
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		if r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/typing") {
			posts.Add(1)
			release := make(chan struct{})
			held <- release
			select {
			case <-release:
			case <-stop:
				return
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stop) })
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	t.Cleanup(func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
	page.MustEvalOnNewDocument(`(() => {
 window.composerSwaps = 0; window.bodyInputs = 0; window.composerSubmits = 0;
 document.addEventListener('submit', e => { if (e.target.id === 'message-composer') window.composerSubmits++; }, true);
 document.addEventListener('htmx:afterSettle', e => { if (e.target.id === 'message-composer-state') window.composerSwaps++; });
 document.addEventListener('input', e => { if (e.target.id === 'message-body') window.bodyInputs++; });
 window.composerBody = () => { const b = document.getElementById('message-body');
  return JSON.stringify([b.value, b.selectionStart, b.selectionEnd, document.activeElement === b, b.getAttribute('aria-invalid')]); };
})()`)
	page.MustNavigate(server.URL + view.ChannelURL("acme", place.Channel)).MustWaitLoad()
	next := func() chan struct{} {
		t.Helper()
		select {
		case release := <-held:
			return release
		case <-ctx.Done():
			t.Fatal("the post did not arrive", ctx.Err())
			return nil
		}
	}
	return page, next, &posts
}

// release answers the held post and waits until its answer has settled.
func releaseHeld(page *rod.Page, release chan struct{}) {
	swaps := page.MustEval(`() => window.composerSwaps`).Int()
	close(release)
	page.MustWait(`n => window.composerSwaps > n && !document.querySelector('#message-composer button').disabled`, swaps)
}

func testComposerInFlightBrowser(t *testing.T, browser *rod.Browser) {
	t.Run("typing", func(t *testing.T) {
		page, next, posts := heldComposer(t, browser)
		body := page.MustElement("#message-body")
		// A 422 sets the textarea's invalid state and keeps its text.
		page.MustEval(`() => { const body = document.getElementById('message-body'); body.value = '  '; body.focus(); }`)
		page.Keyboard.MustType(input.Enter)
		releaseHeld(page, next())
		state := func() string { return page.MustEval(`() => window.composerBody()`).Str() }
		if got := state(); got != `["  ",2,2,true,"true"]` {
			t.Fatalf("after a 422: %s", got)
		}
		// A line break in the sent text: htmx's parameters keep the
		// textarea's LF, not the CRLF of the encoded request.
		page.MustEval(`() => { const body = document.getElementById('message-body'); body.value = 'first\nline'; body.setSelectionRange(10, 10); }`)
		page.Keyboard.MustType(input.Enter)
		release := next()
		if problem := page.MustEval(`() => {
 const body = document.getElementById('message-body'), sending = document.querySelector('#message-sending span');
 if (body.disabled || document.activeElement !== body) return 'textarea disabled or unfocused in flight';
 if (!document.querySelector('#message-composer button').disabled) return 'send button enabled in flight';
 if (document.getElementById('message-sending').getAttribute('role') !== 'status' || !sending.checkVisibility() || sending.textContent !== 'Sending…') return 'no visible polite sending state';
 return '';
}`).Str(); problem != "" {
			t.Fatal(problem)
		}
		page.MustInsertText("second")
		// Enter in flight neither sends again nor adds a line break.
		page.Keyboard.MustType(input.Enter)
		inputs := page.MustEval(`() => window.bodyInputs`).Int()
		releaseHeld(page, release)
		if got := state(); got != `["second",6,6,true,"false"]` {
			t.Fatalf("after success: %s", got)
		}
		if page.MustEval(`n => window.bodyInputs !== n || document.querySelector('#message-sending span').checkVisibility()`, inputs).Bool() {
			t.Fatal("cleanup fired input, or the sending state stayed")
		}
		// Sent from the button: a selection in the remaining text survives,
		// and focus returns to the textarea.
		page.MustElement("#message-composer button").MustClick()
		release = next()
		page.MustEval(`() => { const body = document.getElementById('message-body');
 body.setRangeText(' more', 6, 6, 'end'); body.setSelectionRange(8, 10); }`)
		releaseHeld(page, release)
		if got := state(); got != `[" more",2,4,true,"false"]` {
			t.Fatalf("after a button send: %s", got)
		}
		// Text that no longer starts with the sent text is left alone.
		page.MustEval(`() => { document.getElementById('message-body').value = 'third'; }`)
		page.Keyboard.MustType(input.Enter)
		release = next()
		page.MustEval(`() => document.getElementById('message-body').setRangeText('T', 0, 1, 'preserve')`)
		releaseHeld(page, release)
		if got := body.MustProperty("value").Str(); got != "Third" {
			t.Fatalf("edited text became %q", got)
		}
		if posts.Load() != 4 || body.MustProperty("disabled").Bool() {
			t.Fatalf("%d posts, want 4", posts.Load())
		}
	})
	t.Run("composition", func(t *testing.T) {
		page, next, posts := heldComposer(t, browser)
		page.MustEval(`() => { const body = document.getElementById('message-body'); body.value = 'sent'; body.focus(); }`)
		page.Keyboard.MustType(input.Enter)
		release := next()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		// A real composition through the browser's IME path.
		must(proto.InputImeSetComposition{Text: "に", SelectionStart: 1, SelectionEnd: 1}.Call(page))
		releaseHeld(page, release)
		if got := page.MustEval(`() => document.getElementById('message-body').value`).Str(); got != "sentに" {
			t.Fatalf("success during composition changed the textarea to %q", got)
		}
		must(proto.InputImeSetComposition{Text: "にほん", SelectionStart: 3, SelectionEnd: 3}.Call(page))
		// The confirming Enter belongs to the composition and sends nothing.
		enter := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13}
		must(enter.Call(page))
		must(proto.InputInsertText{Text: "日本"}.Call(page))
		enter.Type = proto.InputDispatchKeyEventTypeKeyUp
		must(enter.Call(page))
		if got := page.MustEval(`() => window.composerBody()`).Str(); got != `["日本",2,2,true,"false"]` {
			t.Fatalf("after the composition: %s", got)
		}
		// requestSubmit would have dispatched submit within the key event.
		if submits := page.MustEval(`() => window.composerSubmits`).Int(); submits != 1 || posts.Load() != 1 {
			t.Fatalf("%d submits and %d posts, want only the first", submits, posts.Load())
		}
	})
}
