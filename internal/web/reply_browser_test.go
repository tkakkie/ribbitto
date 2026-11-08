package web

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testReplySubmitBrowser(t *testing.T, browser *rod.Browser) {
	for _, cancel := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			page, _, _, _ := pendingBrowserFixture(t, browser, false, func(snapshot *view.ChannelPage, _ *http.ServeMux) {
				snapshot.Reply = &view.Message{ID: kernel.ID{9}, Handle: "owner", Body: "Original"}
			})
			result := page.MustEval(`cancel => {
    const form = document.getElementById('message-composer');
    form.elements.body.value = 'Draft answer';
    let native = null, requests = 0, sending = 0;
    form.addEventListener("message-sending", () => sending++);
    document.addEventListener('htmx:beforeRequest', e => { requests++; e.preventDefault(); });
    form.addEventListener('submit', e => {
     native = { reply: form.elements.reply.value, body: form.elements.body.value, submitter: e.submitter?.name };
     e.preventDefault(); e.stopImmediatePropagation();
    }, true);
    const submitter = cancel ? form.querySelector('[name=clear_reply]') : form.querySelector('button[type=submit]:not([name]):not([form])');
    form.requestSubmit(submitter);
    return { native, requests, sending, pending: document.querySelectorAll('[data-pending-form]').length, body: form.elements.body.value };
   }`, cancel)
			if result.Get("native.reply").Str() != realtime.FormatPayloadID(kernel.ID{9}) || result.Get("native.body").Str() != "Draft answer" || result.Get("requests").Int() != 0 || result.Get("pending").Int() != 0 || result.Get("body").Str() != "Draft answer" {
				t.Fatalf("reply submit intercepted or lost draft/target: %s", result.JSON("", ""))
			}
			if cancel && (result.Get("native.submitter").Str() != "clear_reply" || result.Get("sending").Int() != 0) {
				t.Fatal("cancel entered the send path or lost its submitter")
			}
		})
	}
}
