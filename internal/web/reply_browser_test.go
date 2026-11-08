package web

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
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

func testReplyEnhancedBrowser(t *testing.T, browser *rod.Browser) {
	testReplyQuoteActionsBrowser(t, browser)
	for _, outcome := range []string{"success", "retry", "change", "cancel", "cancel-quote"} {
		t.Run(outcome, func(t *testing.T) {
			original := view.Message{ID: kernel.ID{9}, Handle: "owner", Body: "Original"}
			cancelling := outcome == "cancel" || outcome == "cancel-quote"
			var quote *view.Message
			quoteID := ""
			if outcome == "success" || outcome == "cancel-quote" {
				quote = &view.Message{ID: kernel.ID{8}, Handle: "owner", Body: "Quoted"}
				quoteID = realtime.FormatPayloadID(quote.ID)
			}
			page, _, posts, _ := pendingBrowserFixture(t, browser, false, func(snapshot *view.ChannelPage, _ *http.ServeMux) {
				snapshot.Quote = quote
				snapshot.Messages = []view.Message{original, {ID: kernel.ID{10}, Handle: "owner", Body: "Earlier answer", Reply: &view.Reply{URL: view.MessageURL("acme", original.ID), Handle: "owner", Text: "Original"}}}
			})
			page.MustSetViewport(390, 844, 1, true)
			page.MustEvalOnNewDocument(`(() => {
 const viewport = Object.assign(new EventTarget(), {height: 500, offsetTop: 0});
 Object.defineProperty(window, 'visualViewport', {value: viewport});
 window.replyAnswers = 0;
 document.addEventListener('htmx:afterRequest', e => {
  if (['message-composer', 'quote-form'].includes(e.detail.requestConfig?.elt.id)) window.replyAnswers++;
 });
})()`)
			page.MustReload().MustWaitLoad()
			page.MustWait(`() => window.source && !document.querySelector('#read-form.htmx-request')`)
			tabTo := func(selector string) {
				t.Helper()
				for range 80 {
					if page.MustEval(`s => document.activeElement.matches(s)`, selector).Bool() {
						return
					}
					page.Keyboard.MustType(input.Tab)
				}
				t.Fatalf("Tab did not reach %s", selector)
			}
			answer := func(reply browserReply, count int) {
				t.Helper()
				r := receivePending(t, page, posts)
				if r.values.Get("reply") != realtime.FormatPayloadID(kernel.ID{10}) {
					t.Fatal("shared menu retained previous reply identity")
				}
				r.reply <- reply
				page.MustWait(`n => window.replyAnswers === n`, count)
			}
			chip := func(target *view.Message) browserReply {
				return browserReply{status: 200, body: []byte(renderIn(t, "en", view.ComposerQuote(view.ChannelPage{Reply: target, Quote: quote}, false)))}
			}
			// The preview remains a full-width 44 px link at phone width.
			if !page.MustEval(`() => { const r = document.querySelector('li > a[aria-label]')?.getBoundingClientRect(); return r && r.height >= 44 && r.width >= 44; }`).Bool() {
				t.Fatal("reply preview is below the phone target size")
			}
			tabTo(fmt.Sprintf("#message-%x button[popovertarget]", original.ID))
			page.Keyboard.MustType(input.Enter)
			page.MustElement("#message-actions").MustWaitVisible()
			if !page.MustEval(`() => { const r = document.querySelector('#message-actions [name=reply]').getBoundingClientRect(); return r.height >= 44 && r.width >= 44; }`).Bool() {
				t.Fatal("menu Reply is below the phone target size")
			}
			tabTo("#message-actions [name=reply]")
			page.Keyboard.MustType(input.Enter)
			selected := receivePending(t, page, posts)
			if selected.values.Get("reply") != realtime.FormatPayloadID(original.ID) || selected.values.Get("quote") != quoteID {
				t.Fatal("menu lost selected reply or existing quote")
			}
			selected.reply <- chip(&original)
			page.MustWait(`() => window.replyAnswers === 1 && document.activeElement.name === 'clear_reply'`)
			if !page.MustEval(`() => {
 const target = document.getElementById('composer-reply-description');
 const cancel = document.querySelector('#message-composer [name=clear_reply]');
 const b = document.getElementById('message-body');
 b.focus();
 const visible = e => { const r = e.getBoundingClientRect(); return r.top >= visualViewport.offsetTop && r.bottom <= visualViewport.offsetTop + visualViewport.height; };
 const r = cancel.getBoundingClientRect();
 return target.getAttribute('role') === 'status' && b.getAttribute('aria-describedby').includes(target.id) && visible(target) && visible(b) && visible(cancel) && r.width >= 44 && r.height >= 44;
}`).Bool() {
				t.Fatalf("reply target/composer/cancel not visible or operable above emulated keyboard: %s", page.MustEval(`() => Object.fromEntries(['composer-reply-description', 'message-body', 'composer-quote', 'organization-stream'].map(id => {const e = document.getElementById(id); return [id, {rect: e.getBoundingClientRect().toJSON(), describedby: e.getAttribute('aria-describedby'), style: e.getAttribute('style')}];}))`).JSON("", ""))
			}
			page.MustInsertText("Draft answer")
			key := page.MustEval(`() => document.getElementById('message-composer').elements.idempotency_key.value`).Str()
			tabTo("#message-composer button[type=submit]:not([name]):not([form])")
			page.Keyboard.MustType(input.Enter)
			first := receivePending(t, page, posts)
			if first.values.Get("reply") != realtime.FormatPayloadID(original.ID) || first.values.Get("idempotency_key") != key || first.values.Get("body") != "Draft answer" {
				t.Fatal("enhanced send lost reply, body or key")
			}
			count := 2
			if outcome != "success" {
				first.reply <- browserReply{status: 500, body: []byte("unconfirmed")}
				page.MustWait(`() => window.replyAnswers === 2`)
				if !page.MustEval(`quoted => { const f = document.getElementById('message-composer'); return f.elements.body.value === 'Draft answer' && f.elements.reply && Boolean(f.elements.quote) === quoted && !document.querySelector('[data-pending-form]'); }`, quote != nil).Bool() {
					t.Fatal("failure lost text/target/quote or entered pending queue")
				}
				if cancelling {
					tabTo("#message-composer [name=clear_reply]")
					page.Keyboard.MustType(input.Enter)
					cancel := receivePending(t, page, posts)
					if cancel.values.Get("clear_reply") != "1" || cancel.values.Get("idempotency_key") != key {
						t.Fatal("cancel changed the unconfirmed key")
					}
					cancel.reply <- browserReply{status: 200, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{Quote: quote, QuoteUpdate: true, EventCursor: new(int64(42)), IdempotencyKey: &key})))}
					count++
					page.MustWait(`n => window.replyAnswers === n`, count)
					if !page.MustEval(`quoted => { const f = document.getElementById('message-composer'); return !f.elements.reply && Boolean(f.elements.quote) === quoted && f.elements.body.value === 'Draft answer'; }`, quote != nil).Bool() {
						t.Fatal("cancel lost text/quote or retained reply")
					}
				} else if outcome == "change" {
					// Choose another reply through the same shared menu/form.
					tabTo(fmt.Sprintf("#message-%x button[popovertarget]", kernel.ID{10}))
					page.Keyboard.MustType(input.Enter)
					tabTo("#message-actions [name=reply]")
					page.Keyboard.MustType(input.Enter)
					changed := original
					changed.ID = kernel.ID{10}
					count++
					answer(chip(&changed), count)
					if page.MustEval(`() => document.getElementById('message-composer').elements.reply?.value`).Str() != realtime.FormatPayloadID(changed.ID) {
						t.Fatal("changed-target fragment lost reply")
					}
				}
				tabTo("#message-composer button[type=submit]:not([name]):not([form])")
				page.Keyboard.MustType(input.Enter)
				retry := receivePending(t, page, posts)
				want := first.values.Get("reply")
				if cancelling {
					want = ""
				}
				if outcome == "change" {
					want = realtime.FormatPayloadID(kernel.ID{10})
				}
				if retry.values.Get("reply") != want || retry.values.Get("idempotency_key") != key || retry.values.Get("body") != "Draft answer" {
					t.Fatal("retry lost original key/text or current target")
				}
				first = retry
				count++
			}
			status := 200
			if outcome == "change" || cancelling {
				status = 409
			}
			first.reply <- browserReply{status: status, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{QuoteUpdate: true, IdempotencyKey: new("fresh-key")})))}
			page.MustWait(`n => window.replyAnswers === n`, count)
			if !page.MustEval(`success => { const f = document.getElementById('message-composer'); return !f.elements.reply && f.elements.idempotency_key.value === 'fresh-key' && f.elements.body.value === (success ? '' : 'Draft answer'); }`, status == 200).Bool() {
				t.Fatal("success/conflict did not clear or retain the expected composer state")
			}
		})
	}
}

func testReplyQuoteActionsBrowser(t *testing.T, browser *rod.Browser) {
	for _, action := range []string{"menu-quote", "remove-quote", "quote-part", "reply-part", "reply-empty-part"} {
		t.Run(action, func(t *testing.T) {
			original := view.Message{ID: kernel.ID{9}, Handle: "owner", Body: "Original"}
			quoted := view.Message{ID: kernel.ID{8}, Handle: "owner", Body: "Quoted text"}
			page, _, posts, _ := pendingBrowserFixture(t, browser, false, func(snapshot *view.ChannelPage, _ *http.ServeMux) {
				snapshot.Reply, snapshot.Quote = &original, &quoted
				snapshot.Messages = []view.Message{original, quoted}
				if action == "reply-part" || action == "reply-empty-part" {
					snapshot.QuoteText = new("Quoted")
				}
			})
			page.MustEval(`() => {
 window.quoteAnswers = 0;
 document.addEventListener('htmx:afterRequest', e => { if (e.detail.requestConfig?.elt.id === 'quote-form') window.quoteAnswers++; });
 document.getElementById('message-body').value = 'Answer';
}`)
			part := "text"
			if action == "reply-empty-part" {
				part = ""
			}
			switch action {
			case "menu-quote", "reply-part", "reply-empty-part":
				page.MustElement(fmt.Sprintf("#message-%x button[popovertarget]", quoted.ID)).MustClick()
				name := "quote"
				if action != "menu-quote" {
					name = "reply"
					page.MustEval(`text => document.getElementById('quote-text').value = text`, part)
				}
				page.MustElement("#message-actions [name=" + name + "]").MustClick()
			case "remove-quote":
				page.MustElement("#composer-quote button[form=quote-form]:not([name])").MustClick()
			case "quote-part":
				page.MustElement("#composer-quote [name=part]").MustClick()
			}
			request := receivePending(t, page, posts)
			want := realtime.FormatPayloadID(original.ID)
			if action == "reply-part" || action == "reply-empty-part" {
				want = realtime.FormatPayloadID(quoted.ID)
				if !request.values.Has("quote_text") || request.values.Get("quote_text") != part {
					t.Fatalf("Reply lost present quote_text: %v", request.values)
				}
			}
			if request.values.Get("reply") != want {
				t.Fatalf("quote action lost reply target: %v", request.values)
			}
			fragment := view.ChannelPage{Reply: &original, Quote: &quoted}
			switch action {
			case "remove-quote":
				fragment.Quote = nil
			case "quote-part":
				fragment.QuoteText = new(quoted.Body)
			case "reply-part", "reply-empty-part":
				fragment.Reply, fragment.QuoteText = &quoted, &part
			}
			status := 200
			if action == "reply-empty-part" {
				status, fragment.QuoteInvalid = 422, true
			}
			request.reply <- browserReply{status: status, body: []byte(renderIn(t, "en", view.ComposerQuote(fragment, false)))}
			page.MustWait(`() => window.quoteAnswers === 1`)
			if page.MustEval(`() => document.getElementById('message-composer').elements.reply?.value`).Str() != want {
				t.Fatal("quote response lost reply chip")
			}
			page.MustElement("#message-composer button[type=submit]:not([name]):not([form])").MustClick()
			sent := receivePending(t, page, posts)
			if sent.values.Get("reply") != want || sent.values.Get("body") != "Answer" {
				t.Fatal("send after quote action lost reply or text")
			}
			if (action == "reply-part" || action == "reply-empty-part") && (!sent.values.Has("quote_text") || sent.values.Get("quote_text") != part) {
				t.Fatal("send after Reply lost the current quote part")
			}
			sent.reply <- browserReply{status: 500, body: []byte("unconfirmed")}
		})
	}
}
