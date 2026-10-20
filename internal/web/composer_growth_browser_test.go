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
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testComposerGrowthBrowser(t *testing.T, browser *rod.Browser) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][3]int{{1280, 800, 0}, {390, 844, 0}, {390, 500, 0}, {390, 600, 34}, {390, 440, 34}, {320, 200, 0}, {1280, 400, 0}} {
		t.Run(fmt.Sprintf("%dx%d/inset%d", size[0], size[1], size[2]), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			seqs := make([]int, 40)
			for i := range seqs {
				seqs[i] = i + 1
			}
			topic, cursor := kernel.ID{2}, int64(40)
			key := "00000000-0000-4000-8000-000000000001"
			snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
				Current: view.Channel{ID: kernel.ID{1}, Name: "general"}, EventCursor: &cursor, IdempotencyKey: &key, Messages: streamTestMessages(topic, seqs...)}
			events := make(chan realtime.Outgoing, 1)
			mux := http.NewServeMux()
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/events"):
					w.Header().Set("Content-Type", "text/event-stream")
					sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
					for err := sender.Heartbeat(r.Context()); err == nil; {
						select {
						case out := <-events:
							err = sender.Send(r.Context(), out)
						case <-r.Context().Done():
							return
						}
					}
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/quote"):
					response := snapshot
					if r.FormValue("quote") != "" {
						response.Quote = &snapshot.Messages[0]
					}
					templ.Handler(view.ComposerQuote(response, false)).ServeHTTP(w, r)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/typing"):
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost:
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					response := snapshot
					if len(r.FormValue("body")) > 4000 {
						response.BodyError = "message.error.body"
						w.WriteHeader(http.StatusUnprocessableEntity)
					} else {
						response.PostedMessageID = &kernel.ID{41}
						response.PostedTopicID, response.PostedEventSeq = topic, 41
					}
					templ.Handler(view.MessageComposer(response)).ServeHTTP(w, r)
				default:
					response := snapshot
					if r.URL.Query().Has("ordinary") {
						response.IdempotencyKey = nil
					}
					if r.URL.Query().Has("quote") {
						response.Quote = &snapshot.Messages[0]
					}
					if r.URL.Query().Has("preset") {
						response.Body = "preset\nsecond\nthird\nfourth"
					}
					templ.Handler(view.ChannelScreen("/static/css/app.css", response)).ServeHTTP(w, r)
				}
			})
			server := httptest.NewServer(catalogues.Middleware(mux))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "composer growth")
			defer closePage()
			page = page.WithPanic(func(v any) { t.Fatal(v) })
			_, err := page.Call(ctx, string(page.SessionID), "Emulation.setSafeAreaInsetsOverride", map[string]any{"insets": map[string]int{"bottom": size[2]}})
			if err != nil {
				t.Fatal(err)
			}
			page.MustSetViewport(size[0], size[1], 1, false)
			page.MustNavigate(server.URL).MustWaitLoad()
			// Two frames include layout, ResizeObserver and its resulting scroll.
			settle := func() {
				page.MustEval(`() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
			}
			check := func(what, js string, args ...any) {
				t.Helper()
				settle()
				if problem := page.MustEval(js, args...).Str(); problem != "" {
					t.Fatalf("%s: %s", what, problem)
				}
			}
			set := func(text string) {
				page.MustEval(`text => { const b = document.getElementById('message-body'); b.value = text;
					b.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertFromPaste'})); }`, text)
				settle()
			}
			rest := 38
			if size[0] < 852 {
				rest = 44
			}
			check("resting height", `want => { const b = document.getElementById('message-body');
				return b.clientHeight === want && b.scrollHeight === b.clientHeight ? '' : 'height/scroll ' + b.clientHeight + '/' + b.scrollHeight; }`, rest)
			restingHeight := page.MustEval(`() => document.getElementById('message-body').clientHeight`).Int()
			// Compare with the actual old layout, including the safe area, rather
			// than deriving the floor's permitted list height from the new CSS.
			page.MustEval(`() => document.getElementById('message-body').style.fieldSizing = 'fixed'`)
			settle()
			oldListHeight := page.MustEval(`() => document.getElementById('message-list').clientHeight`).Int()
			check("three-row fallback", `() => document.getElementById('message-body').clientHeight === 82 ? '' : 'not 82 px'`)
			set(strings.Repeat("fallback\n", 30))
			check("fallback inner scroll", `() => { const b = document.getElementById('message-body');
				return b.clientHeight === 82 && b.scrollHeight > b.clientHeight ? '' : 'fallback grew'; }`)
			page.MustEval(`() => document.getElementById('message-body').style.removeProperty('field-sizing')`)
			set("")
			page.MustElement("#message-body").MustInput("typed\nsecond")
			check("typed growth", `rest => { const b = document.getElementById('message-body');
				return b.clientHeight > rest && b.scrollHeight === b.clientHeight ? '' : 'did not grow without scrolling'; }`, rest)
			set("paste\nsecond\nthird\nfourth")
			check("paste below cap", `() => { const b = document.getElementById('message-body');
				return b.clientHeight >= 82 && (b.clientHeight === 82 || b.scrollHeight === b.clientHeight) ? '' : 'unexpected inner scroll'; }`)
			set(strings.Repeat("capped\n", 100))
			check("cap and list reserve", `(oldHeight, short, floor) => {
				const b = document.getElementById('message-body'), l = document.getElementById('message-list');
				const cap = short ? 82 : Math.max(82, Math.min(innerHeight / 2, oldHeight + 82 - 128));
				if (b.clientHeight !== Math.floor(cap) || b.scrollHeight <= b.clientHeight) return 'cap/scroll ' + b.clientHeight + '/' + cap;
				if (floor && b.clientHeight !== 82) return 'floor did not win';
				if (l.clientHeight < (floor ? oldHeight : 128)) return 'list too short: ' + l.clientHeight;
				const m = document.getElementById('conversation');
				if (short && (b.clientHeight > 82 || m.scrollHeight <= m.clientHeight || getComputedStyle(m).overflowY !== 'auto')) return 'short conversation cannot scroll';
				return '';
			}`, oldListHeight, size[1] < 436, size[1] == 440 || size[1] < 436)
			if size[1] >= 436 {
				check("column deduction", `(phone, inset) => { const l = document.getElementById('message-list'), b = document.getElementById('message-body');
					const outside = Math.ceil(innerHeight - l.clientHeight - b.clientHeight);
					return outside === (phone ? 209 + inset : 225) ? '' : 'column outside textarea/list: ' + outside; }`, size[0] < 852, size[2])
			}
			page.MustElement("#message-body").MustSelectAllText().MustInput(strings.Repeat("caret\n", 100) + "last line")
			check("caret at end", `() => { const b = document.getElementById('message-body');
				return b.selectionEnd === b.value.length && b.scrollHeight - b.scrollTop - b.clientHeight <= parseFloat(getComputedStyle(b).paddingBottom) + 1 ? '' : 'last line hidden'; }`)
			page.MustEval(`() => document.querySelector('#message-composer button').scrollIntoView({block: 'nearest'})`)
			check("send reachable", `() => { const r = document.querySelector('#message-composer button').getBoundingClientRect();
				return r.top >= 0 && r.bottom <= innerHeight ? '' : 'send outside viewport'; }`)

			set("")
			page.MustEval(`() => { const l = document.getElementById('message-list'); l.scrollTop = l.scrollHeight; }`)
			settle()
			const newest = `() => { const l = document.getElementById('message-list'); return l.scrollHeight - l.scrollTop - l.clientHeight <= 2 ? '' : 'lost newest'; }`
			set(strings.Repeat("grow\n", 8))
			check("newest on growth", newest)
			set("")
			check("newest on shrink", newest)
			page.MustEval(`() => document.getElementById('message-list').scrollTop = 500`)
			settle()
			anchor := page.MustEval(`() => { const l = document.getElementById('message-list'), top = l.getBoundingClientRect().top;
				const item = [...document.querySelectorAll('#message-items > li')].find(i => i.getBoundingClientRect().bottom > top);
				return {id: item.id, offset: item.getBoundingClientRect().top - top}; }`)
			const anchored = `a => Math.abs(document.getElementById(a.id).getBoundingClientRect().top - document.getElementById('message-list').getBoundingClientRect().top - a.offset) <= 1 ? '' : 'reader moved'`
			set(strings.Repeat("grow\n", 8))
			check("reader on growth", anchored, anchor)
			set("")
			check("reader on shrink", anchored, anchor)
			page.MustEval(`() => { const b = document.getElementById('message-body'); b.dispatchEvent(new CompositionEvent('compositionstart', {bubbles: true}));
				b.value = '日本語\n変換中\n三行目'; b.dispatchEvent(new InputEvent('input', {bubbles: true, isComposing: true})); }`)
			check("composition grows", `() => document.getElementById('message-body').clientHeight === 82 ? '' : 'composition did not grow'`)
			page.MustEval(`() => document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionend', {bubbles: true}))`)
			set(strings.Repeat("invalid\n", 600))
			page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
			page.MustWait(`() => document.getElementById('message-body').getAttribute('aria-invalid') === 'true' && !document.querySelector('.htmx-settling')`)
			check("422 retains grown draft", `rest => { const b = document.getElementById('message-body');
				return b.value.startsWith('invalid\n') && b.clientHeight >= 82 && b.clientHeight > rest ? '' : 'draft lost or height reset'; }`, restingHeight)
			set("posted\nsecond\nthird")
			page.MustEval(`() => { document.getElementById('message-list').scrollTop = 0; document.getElementById('message-composer').requestSubmit(); }`)
			page.MustWait(`() => document.getElementById('message-body').value === '' && !!document.querySelector('#message-pending [data-state="confirmed"]') && !document.querySelector('.htmx-settling')`)
			events <- realtime.Outgoing{ID: 41, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 41)[0]))}
			page.MustWait(`() => !!document.querySelector('#message-items > li[data-event-seq="41"]') && (` + newest + `)() === ''`)
			check("send resets height", `rest => document.getElementById('message-body').clientHeight === rest ? '' : 'height not reset'`, rest)
			// Delivery before an ordinary response exercises the other ordering:
			// clearing the grown field must not undo scrollPosted during settle.
			page.MustNavigate(server.URL + "?ordinary").MustWaitLoad()
			events <- realtime.Outgoing{ID: 41, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 41)[0]))}
			page.MustWait(`() => !!document.querySelector('#message-items > li[data-event-seq="41"]')`)
			set("ordinary\nsecond\nthird")
			page.MustEval(`() => { document.getElementById('message-list').scrollTop = 0; document.getElementById('message-composer').requestSubmit(); }`)
			page.MustWait(`() => document.getElementById('message-body').value === '' && !document.querySelector('.htmx-settling')`)
			check("ordinary post wins over resize", newest)
			page.MustNavigate(server.URL + "?preset").MustWaitLoad()
			check("preset grows on load", `() => document.getElementById('message-body').clientHeight >= 82 ? '' : 'preset did not grow'`)
			page.MustSetViewport(size[0], size[1]-50, 1, false)
			check("newest on viewport resize", newest)
			if size[0] == 390 && size[1] == 600 {
				page.MustSetViewport(size[0], size[1], 1, false)
				const quoteSpace = `() => {
					const b = document.getElementById('message-body'), l = document.getElementById('message-list');
					if (b.clientHeight < 82) return 'less than three lines: ' + b.clientHeight;
					if (l.clientHeight < 128) return 'list reserve lost: ' + l.clientHeight;
					const q = document.getElementById('composer-quote').getBoundingClientRect().height;
					const extra = q ? q + parseFloat(getComputedStyle(b.form).rowGap) : 0;
					const cap = Math.max(82, Math.min(innerHeight / 2, innerHeight - 337 - 34 - extra));
					if (b.scrollHeight > b.clientHeight && b.clientHeight !== Math.floor(cap)) return 'quote cap ' + b.clientHeight + '/' + cap;
					return '';
				}`
				for _, query := range []string{"", "?quote"} {
					page.MustNavigate(server.URL + query).MustWaitLoad()
					set("first\nsecond\nthird")
					check("three lines on load "+query, quoteSpace)
					set(strings.Repeat("capped\n", 100))
					check("quote cap on load "+query, quoteSpace)
				}
				setQuote := func(quoted bool) {
					// Submit without scrolling an offscreen message action into view.
					page.MustEval(`quoted => {
						const button = document.querySelector(quoted ? '#message-items [name=quote]' : '#composer-quote button');
						document.getElementById('quote-form').requestSubmit(button);
					}`, quoted)
					page.MustWait(`quoted => !!document.querySelector('#composer-quote input') === quoted && !document.querySelector('.htmx-settling')`, quoted)
					settle()
				}
				setQuote(false)
				// Three lines resize the list on chip swaps; capped text trades
				// textarea space for the chip. Both must retain the same anchor.
				for _, text := range []string{"first\nsecond\nthird", strings.Repeat("capped\n", 100)} {
					set(text)
					for _, atEnd := range []bool{true, false} {
						page.MustEval(`end => { const l = document.getElementById('message-list'); l.scrollTop = end ? l.scrollHeight : 500; }`, atEnd)
						settle()
						quoteAnchor := page.MustEval(`end => {
							const r = document.getElementById('message-list').getBoundingClientRect();
							const items = [...document.querySelectorAll('#message-items > li')];
							const item = end ? items.at(-1) : items.find(i => i.getBoundingClientRect().bottom > r.top);
							return {id: item.id, end, offset: end ? item.getBoundingClientRect().bottom - r.bottom : item.getBoundingClientRect().top - r.top};
						}`, atEnd)
						for _, quoted := range []bool{true, false} {
							setQuote(quoted)
							check(fmt.Sprintf("quote=%t newest=%t anchor", quoted, atEnd), `a => {
								const r = document.getElementById('message-list').getBoundingClientRect(), i = document.getElementById(a.id).getBoundingClientRect();
								return Math.abs((a.end ? i.bottom - r.bottom : i.top - r.top) - a.offset) <= 1 ? '' : 'quote moved reader by more than 1 px';
							}`, quoteAnchor)
							check("space after quote swap", quoteSpace)
						}
					}
				}
				// A synthetic visual viewport leaves layout/dvh unchanged, like
				// Safari's keyboard. It does not emulate native keyboard panning.
				page.MustEvalOnNewDocument(`(() => {
					const viewport = location.search.includes('noViewport') ? undefined :
						Object.assign(new EventTarget(), {height: location.search.includes('equalViewport') ? innerHeight : 540, offsetTop: 0});
					Object.defineProperty(window, 'visualViewport', {value: viewport});
				})()`)
				page.MustNavigate(server.URL).MustWaitLoad()
				check("visual viewport on load", `() => document.getElementById('organization-stream').style.getPropertyValue('--composer-viewport-h') === '540px' ? '' : 'height missing'`)
				page.MustElement("#message-body").MustInput(strings.Repeat("paste\n", 20) + "last line")
				const visible = `() => {
					const v = visualViewport, b = document.getElementById('message-body'), r = b.getBoundingClientRect();
					const label = document.querySelector('label[for="message-body"]').getBoundingClientRect();
					const cap = Math.max(82, Math.min(v.height / 2, v.height - 337 - 34));
					if (innerHeight !== 600 || b.clientHeight !== Math.floor(cap) || b.clientHeight > v.height / 2) return 'visual cap ' + b.clientHeight + '/' + cap;
					if (label.top < v.offsetTop || r.top < v.offsetTop || r.bottom > v.offsetTop + v.height) return 'composer outside visible viewport';
					if (b.scrollHeight <= b.clientHeight || b.scrollHeight - b.scrollTop - b.clientHeight > parseFloat(getComputedStyle(b).paddingBottom) + 1) return 'inner scroll/caret hidden';
					return '';
				}`
				check("initial visual cap", visible)
				page.MustEval(`() => { visualViewport.height = 270; visualViewport.offsetTop = 120; visualViewport.dispatchEvent(new Event('resize')); }`)
				check("keyboard cap and caret", visible)
				check("newest after keyboard shrink", newest)
				page.MustEval(`() => { visualViewport.offsetTop = 0; visualViewport.dispatchEvent(new Event('scroll')); }`)
				check("keyboard pan", visible)
				check("newest after keyboard pan", newest)
				page.MustEval(`() => { visualViewport.height = 540; visualViewport.dispatchEvent(new Event('resize')); }`)
				check("keyboard expands", visible)
				check("newest after keyboard expansion", newest)
				viewportStep := pageStep(ctx, page)
				fallback := func(what string, action func(*rod.Page)) {
					t.Helper()
					viewportStep(t, what, func(p *rod.Page) {
						action(p)
						p.MustEval(`() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
						if problem := p.MustEval(`() => {
							const s = document.getElementById('organization-stream').style, b = document.getElementById('message-body');
							if (document.documentElement.style.length || s.getPropertyValue('--composer-viewport-h') || s.getPropertyValue('--composer-viewport-top')) return 'viewport override remains';
							const cap = Math.max(82, Math.min(innerHeight / 2, innerHeight - 337 - 34));
							return Math.abs(parseFloat(getComputedStyle(b).maxHeight) - cap) <= 0.01 ? '' : 'fallback cap incorrect';
						}`).Str(); problem != "" {
							panic(problem)
						}
					})
				}
				fallback("keyboard close removes viewport overrides", func(p *rod.Page) {
					p.MustEval(`() => { visualViewport.height = innerHeight; visualViewport.offsetTop = 0; window.dispatchEvent(new Event('orientationchange')); }`)
				})
				fallback("equal visual viewport on load uses dvh", func(p *rod.Page) {
					p.MustNavigate(server.URL + "?equalViewport").MustWaitLoad()
				})
				fallback("subpixel visual viewport uses dvh", func(p *rod.Page) {
					p.MustEval(`() => { visualViewport.height = innerHeight - 0.5; visualViewport.offsetTop = 0.5; visualViewport.dispatchEvent(new Event('resize')); }`)
				})
				page.MustNavigate(server.URL + "?noViewport").MustWaitLoad()
				set(strings.Repeat("fallback\n", 100))
				check("no visual viewport uses dvh", `() => {
					const b = document.getElementById('message-body');
					return !document.getElementById('organization-stream').style.getPropertyValue('--composer-viewport-h') &&
						b.clientHeight === Math.floor(Math.max(82, Math.min(innerHeight / 2, innerHeight - 337 - 34))) &&
						b.scrollHeight > b.clientHeight ? '' : 'dvh fallback failed';
				}`)
				check("newest without visual viewport", newest)
			}
		})
	}
}
