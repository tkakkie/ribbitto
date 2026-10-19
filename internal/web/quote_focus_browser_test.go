package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testQuoteFocusBrowser(t *testing.T, browser *rod.Browser, width int) {
	const remove = `#composer-quote button:not([name])`
	const part = `#composer-quote button[name=part]`
	const send = `#message-composer button[type=submit]:not([form])`
	type focusCase struct {
		name, focus, want string
		status            int
		partChip          bool
	}
	cases := []focusCase{
		{"whole", "#message-body", remove, 200, false},
		{"whole-tab", remove, part, 0, false},
		{"part", part, "#quote-text", 200, false},
		{"part-tab-remove", "#quote-text", remove, 0, true},
		{"remove-tab-body", remove, "#message-body", 0, true},
		{"body-tab-send", "#message-body", send, 0, true},
		{"remove", remove, "#message-body", 200, false},
	}
	for _, status := range []int{422, 409} {
		for _, control := range []struct {
			name, selector string
			partChip       bool
		}{
			{"caret", "#quote-text", true}, {"selection", "#quote-text", true}, {"body", "#message-body", true},
			{"remove", remove, false}, {"part", part, false}, {"disabled-send", send, true}, {"blurred-send", send, true}, {"send", send, true},
		} {
			want := control.selector
			if strings.HasSuffix(control.name, "send") {
				want = "#message-body"
			}
			cases = append(cases, focusCase{fmt.Sprintf("%d-%s", status, control.name), control.selector, want, status, control.partChip})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			// Each case owns its transport; typing never shares a rate limiter.
			snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}},
				Composer: new("generation"), IdempotencyKey: new("00000000-0000-4000-8000-000000000001"), EventCursor: new(int64(42)),
				Quote: &view.Message{ID: kernel.ID{2}, Body: "page original", Handle: "alice", DisplayName: "Alice"}}
			if tc.partChip {
				snapshot.QuoteText = new("page original")
			}
			answer := snapshot
			if tc.name == "part" {
				answer.QuoteText = new("page original")
			}
			if tc.name == "remove" {
				answer.Quote = nil
			}
			if tc.status >= 400 {
				answer.QuoteUpdate = true
				if answer.QuoteText != nil {
					answer.QuoteText = new("server text")
					answer.QuoteInvalid = tc.status == 422
				}
			}
			replies := make(chan browserReply, 1)
			mux := http.NewServeMux()
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/setup":
					w.WriteHeader(200)
				case strings.HasSuffix(r.URL.Path, "/events"):
					idleStream(w, r)
				case strings.HasSuffix(r.URL.Path, "/typing"), strings.HasSuffix(r.URL.Path, "/read"):
					w.WriteHeader(204)
				case r.Method == http.MethodPost:
					select {
					case reply := <-replies:
						w.WriteHeader(reply.status)
						_, _ = w.Write(reply.body)
					case <-r.Context().Done():
					}
				default:
					templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
				}
			})
			catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(catalogues.Middleware(mux))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "quote focus page")
			defer closePage()
			step := pageStep(ctx, page)
			step(t, "setup: storage cleared before composer scripts load", func(p *rod.Page) {
				if err := navigateLoaded(p, server.URL+"/setup"); err != nil {
					panic(err)
				}
				p.MustEval(`() => localStorage.clear()`)
				p.MustSetViewport(width, 844, 1, width < 768)
				if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: width < 768}).Call(p); err != nil {
					panic(err)
				}
			})
			step(t, "navigation: composer visible and response counters installed", func(p *rod.Page) {
				if err := navigateLoaded(p, server.URL); err != nil {
					panic(err)
				}
				p.MustElement("#message-body").MustWaitVisible()
				p.MustEval(`() => {
 window.quoteSettles=0; window.focusAnswers=0; window.oldChip=document.getElementById('composer-quote');
 document.addEventListener('htmx:afterSettle', e => { if(e.target.id==='composer-quote') window.quoteSettles++; });
 document.addEventListener('htmx:afterRequest', e => { if(['message-composer','quote-form'].includes(e.target.id)) window.focusAnswers++; });
}`)
			})
			step(t, "prepare: focus on "+tc.focus+" with known caret/selection", func(p *rod.Page) {
				p.MustElement(tc.focus).MustFocus()
				if tc.focus == "#quote-text" {
					p.MustEval(`selection => document.getElementById('quote-text').setSelectionRange(selection ? 0 : 3, selection ? 5 : 3, selection ? 'backward' : 'forward')`, strings.HasSuffix(tc.name, "selection"))
				}
				p.MustWait(`s => document.activeElement.matches(s)`, tc.focus)
			})
			if tc.status == 0 {
				step(t, "Tab: focus reaches "+tc.want, func(p *rod.Page) {
					p.Keyboard.MustType(input.Tab)
					p.MustWait(`s => document.activeElement.matches(s)`, tc.want)
				})
				return
			}
			step(t, "request: htmx holds response while current control remains available", func(p *rod.Page) {
				form := "quote-form"
				if tc.status >= 400 {
					form = "message-composer"
				}
				p.MustEval(`id => document.getElementById(id).requestSubmit()`, form)
				p.MustWait(`id => document.getElementById(id).classList.contains('htmx-request')`, form)
				if strings.HasSuffix(tc.name, "send") {
					p.MustWait(`s => document.querySelector(s).disabled`, send)
					if strings.HasSuffix(tc.name, "-send") && !strings.Contains(tc.name, "disabled") && !strings.Contains(tc.name, "blurred") {
						// Exercise the still-focused button branch independently of
						// Chrome's frame-dependent blur when htmx disables it.
						p.MustEval(`s => { const b=document.querySelector(s); b.disabled=false; b.focus(); }`, send)
						p.MustWait(`s => document.activeElement.matches(s)`, send)
					}
					if strings.HasSuffix(tc.name, "blurred-send") {
						p.MustEval(`() => document.activeElement.blur()`)
						p.MustWait(`() => document.activeElement === document.body`)
					}
				}
			})
			markup := view.ComposerQuote(answer, false)
			if tc.status >= 400 {
				markup = view.MessageComposer(answer)
			}
			replies <- browserReply{status: tc.status, body: []byte(renderIn(t, "en", markup))}
			step(t, "response: replaced chip settles with focus on "+tc.want, func(p *rod.Page) {
				p.MustWait(`() => window.quoteSettles===1 && window.focusAnswers===1 && document.getElementById('composer-quote')!==window.oldChip`)
				p.MustWait(`s => document.activeElement.matches(s)`, tc.want)
				if tc.focus == "#quote-text" {
					p.MustWait(`selection => { const q=document.getElementById('quote-text'); return q.value==='server text' && q.selectionStart===(selection ? 0 : 3) && q.selectionEnd===(selection ? 5 : 3) && q.selectionDirection===(selection ? 'backward' : 'forward'); }`, strings.HasSuffix(tc.name, "selection"))
				}
			})
		})
	}
}
