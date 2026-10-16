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
	handler, writer, _, _, _, path := keyedComposer(t, false)
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
	page.MustEvalOnNewDocument(`(() => { window.composerAnswers = 0;
 document.addEventListener('htmx:afterRequest', e => { if (e.target.id === 'message-composer') window.composerAnswers++; });
})()`)
	page.MustNavigate(server.URL + path).MustWaitLoad()
	if got := page.MustEval(`() => document.getElementById('message-composer').getAttribute('hx-request')`).Str(); got != `{"timeout":30000}` {
		t.Fatalf("hx-request %q", got)
	}
	// Each composer POST takes the next answer: pass, lost (reaches the
	// handler, then fails), failed, hung, or a status replacing the handler.
	answers, sent := make(chan string, 1), make(chan url.Values, 1)
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
	page.MustEval(`() => { document.getElementById('message-body').value = 'hello'; }`)
	key := state()[1]
	if role := page.MustEval(`() => document.getElementById('message-send-alert').getAttribute('role')`).Str(); role != "alert" {
		t.Fatalf("alert role %q", role)
	}
	for _, step := range []struct {
		answer, mark, alert string
		timeout             bool
	}{
		{answer: "429", alert: notSent},
		{answer: "failed", mark: key, alert: unconfirmed},
		{answer: "hung", mark: key, alert: unconfirmed, timeout: true},
		{answer: "500", mark: key, alert: unconfirmed},
		{answer: "429", mark: key, alert: refused},
	} {
		if step.timeout {
			page.MustEval(`() => document.getElementById('message-composer').setAttribute('hx-request', '{"timeout":200}')`)
		}
		if form := send(step.answer); form.Get("idempotency_key") != key || form.Get("body") != "hello" {
			t.Fatalf("%s: sent %v, want the same key", step.answer, form)
		}
		if got, want := state(), []string{"hello", key, step.mark, step.alert}; !slices.Equal(got, want) {
			t.Fatalf("after %s: %q, want %q", step.answer, got, want)
		}
		page.MustEval(`() => document.getElementById('message-composer').setAttribute('hx-request', '{"timeout":30000}')`)
	}
	send("pass")
	if got := state(); got[0] != "" || got[1] == key || got[2] != "" || got[3] != "" {
		t.Fatalf("after success: %q", got)
	}
	// The first POST is stored but its answer lost; a 404 to the retry,
	// as after the session ended, proves nothing about it.
	key = state()[1]
	page.MustEval(`() => { document.getElementById('message-body').value = 'second'; }`)
	for _, step := range []struct{ answer, alert string }{{"lost", unconfirmed}, {"404", refused}} {
		if form := send(step.answer); form.Get("idempotency_key") != key {
			t.Fatalf("%s: sent %v", step.answer, form)
		}
		if got, want := state(), []string{"second", key, key, step.alert}; !slices.Equal(got, want) {
			t.Fatalf("after %s: %q, want %q", step.answer, got, want)
		}
	}
	send("pass")
	if got := state(); got[0] != "" || got[1] == key || got[2] != "" || got[3] != "" {
		t.Fatalf("after the repeat: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if writer.inserts != 2 {
		t.Fatalf("%d messages stored, want 2", writer.inserts)
	}
}
