package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// Each failure keeps the text and the key; only a 200 or a 409 settles a
// send whose answer was lost, and a refused retry never says it was not
// sent (#1179).
func testComposerFailureBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	handler, writer, _, _, _, path := keyedComposer(t, true)
	// The fake writer is not safe for concurrent use; the test reads it too.
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		mu.Lock()
		defer mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	t.Cleanup(func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
	// Exercise ordinary sending when the secure-context UUID API is unavailable.
	// Latest topics now also render the pending template.
	page.MustEvalOnNewDocument(`(() => {
 Object.defineProperty(crypto, 'randomUUID', {value: undefined}); window.composerAnswers = 0;
 document.addEventListener('htmx:afterRequest', e => { if (e.target.id === 'message-composer') window.composerAnswers++; });
})()`)
	page.MustNavigate(server.URL + path).MustWaitLoad()
	if !page.MustHas("#message-pending-template") {
		t.Fatal("latest topic is missing its pending template")
	}
	if got := page.MustEval(`() => document.getElementById('message-composer').getAttribute('hx-request')`).Str(); got != `{"timeout":30000}` {
		t.Fatalf("hx-request %q", got)
	}
	// Each composer POST takes the next answer: pass, lost (reaches the
	// handler, then fails), failed, hung, or a status replacing the handler.
	answers, sent, held := make(chan string, 1), make(chan url.Values, 1), make(chan struct{})
	router := page.HijackRequests()
	router.MustAdd(server.URL+path, func(h *rod.Hijack) {
		if h.Request.Method() != "POST" {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		form, _ := url.ParseQuery(h.Request.Body())
		sent <- form
		switch answer := <-answers; answer {
		case "pass":
			h.ContinueRequest(&proto.FetchContinueRequest{})
		case "held":
			select {
			case <-held:
			case <-ctx.Done():
			}
			h.ContinueRequest(&proto.FetchContinueRequest{})
		case "lost":
			if err := h.LoadResponse(http.DefaultClient, true); err != nil {
				t.Error(err)
			}
			h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
		case "failed":
			h.Response.Fail(proto.NetworkErrorReasonConnectionRefused)
		case "hung":
			// Only the request's own timeout can end it.
			h.OnError = func(error) {}
			<-ctx.Done()
			h.Response.Fail(proto.NetworkErrorReasonTimedOut)
		default:
			code, _ := strconv.Atoi(answer)
			h.Response.Payload().ResponseCode = code
			// Synthetic rejections have no composer state to swap (422 swaps).
			h.Response.SetHeader("HX-Reswap", "none")
			h.Response.SetBody("refused")
		}
	})
	go router.Run()
	t.Cleanup(func() { _ = router.Stop() })
	state := func() []string {
		got := page.MustEval(`() => { const form = document.getElementById('message-composer');
 return [form.elements.body.value, form.elements.idempotency_key.value, form.dataset.unconfirmedKey ?? '', document.getElementById('message-send-alert').textContent]; }`).Arr()
		values := make([]string, len(got))
		for i, v := range got {
			values[i] = v.Str()
		}
		return values
	}
	send := func(answer string) url.Values {
		t.Helper()
		answers <- answer
		n := page.MustEval(`() => window.composerAnswers`).Int()
		page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
		form := <-sent
		page.MustWait(`n => window.composerAnswers > n`, n)
		return form
	}
	const (
		unconfirmed = "Your message could not be confirmed as sent. Sending again will not post it twice."
		notSent     = "Your message was not sent. Your text is kept."
		refused     = "This retry was refused, and your earlier send is still unconfirmed. Check the conversation before sending again."
	)
	if role := page.MustEval(`() => document.getElementById('message-send-alert').getAttribute('role')`).Str(); role != "alert" {
		t.Fatalf("alert role %q", role)
	}
	// After a success, "second" is stored but its answer lost; a 404 to the
	// retry, as after the session ended, proves nothing about it.
	var key string
	for _, step := range []struct {
		text, answer, alert string
		marked              bool
	}{
		{"hello", "400", notSent, false}, {"hello", "404", notSent, false},
		{"hello", "413", notSent, false}, {"hello", "422", notSent, false},
		{"hello", "429", notSent, false}, {"hello", "failed", unconfirmed, true},
		{"hello", "hung", unconfirmed, true}, {"hello", "500", unconfirmed, true},
		{"hello", "429", refused, true}, {"hello", "pass", "", false},
		{"second", "408", unconfirmed, true}, {"second", "lost", unconfirmed, true},
		{"second", "404", refused, true}, {"second", "pass", "", false},
	} {
		if state()[0] == "" {
			page.MustEval(`text => { document.getElementById('message-body').value = text; }`, step.text)
			key = state()[1]
		}
		timeout := `{"timeout":30000}`
		if step.answer == "hung" {
			timeout = `{"timeout":200}`
		}
		page.MustEval(`timeout => document.getElementById('message-composer').setAttribute('hx-request', timeout)`, timeout)
		if form := send(step.answer); form.Get("idempotency_key") != key || form.Get("body") != step.text {
			t.Fatalf("%s: sent %v, want the same key", step.answer, form)
		}
		want := []string{step.text, key, "", step.alert}
		if step.marked {
			want[2] = key
		}
		got := state()
		if step.answer == "pass" {
			want = []string{"", got[1], "", ""} // empty, with a new key
		}
		if !slices.Equal(got, want) || step.answer == "pass" && got[1] == key {
			t.Fatalf("after %s: %q, want %q", step.answer, got, want)
		}
	}
	// "third" is stored and its answer lost; the retry's 409 arrives during
	// an IME composition, and the stored text leaves only when it ends, with
	// the caret kept on the remaining text (#1173's rules).
	page.MustEval(`() => { document.getElementById('message-body').value = 'third'; }`)
	send("lost")
	page.MustEval(`() => { const body = document.getElementById('message-body'); body.value = 'third world'; body.setSelectionRange(7, 7); body.focus(); }`)
	answers <- "held"
	n := page.MustEval(`() => window.composerAnswers`).Int()
	page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
	<-sent
	if err := (proto.InputImeSetComposition{Text: "に", SelectionStart: 1, SelectionEnd: 1}).Call(page); err != nil {
		t.Fatal(err)
	}
	held <- struct{}{}
	page.MustWait(`n => window.composerAnswers > n`, n)
	if got := state(); got[0] != "third wにorld" || got[3] != "" {
		t.Fatalf("the 409 changed the composition: %q", got)
	}
	if err := (proto.InputInsertText{Text: "日本"}).Call(page); err != nil {
		t.Fatal(err)
	}
	caret := page.MustEval(`() => document.getElementById('message-body').selectionStart`).Int()
	if got := state(); got[0] != " w日本orld" || caret != 4 || got[3] != "Your earlier message was sent. What you typed after it is kept." {
		t.Fatalf("after the composition: %q, caret %d", got, caret)
	}
	mu.Lock()
	defer mu.Unlock()
	if writer.inserts != 3 {
		t.Fatalf("%d messages stored, want 3", writer.inserts)
	}
}
