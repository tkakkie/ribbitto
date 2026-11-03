package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
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
	t.Run("landscape-keyboard", func(t *testing.T) {
		testComposerLandscapeKeyboard(t, browser, catalogues)
	})
}

func testComposerLandscapeKeyboard(t *testing.T, browser *rod.Browser, catalogues *i18n.Catalogues) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	seqs := make([]int, 40)
	for i := range seqs {
		seqs[i] = i + 1
	}
	topic, cursor, key := kernel.ID{2}, int64(40), "00000000-0000-4000-8000-000000000001"
	snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current: view.Channel{ID: kernel.ID{1}, Name: "general"}, EventCursor: &cursor, IdempotencyKey: &key, Messages: streamTestMessages(topic, seqs...)}
	events, releaseInFlight := make(chan realtime.Outgoing, 4), make(chan struct{})
	holdNext := make(chan struct{}, 1)
	var posted atomic.Int64
	posted.Store(cursor)
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
			if r.FormValue("quote") != "" || r.FormValue("part") != "" {
				response.Quote = &snapshot.Messages[0]
			}
			if r.FormValue("part") != "" {
				response.QuoteText = &response.Quote.Body
			}
			templ.Handler(view.ComposerQuote(response, false)).ServeHTTP(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/typing"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			select {
			case <-holdNext:
				select {
				case <-releaseInFlight:
				case <-r.Context().Done():
					return
				}
			default:
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			response, body := snapshot, r.FormValue("body")
			switch {
			case strings.HasPrefix(body, "fail500"):
				w.WriteHeader(http.StatusInternalServerError)
				return
			case len(body) > 4000:
				response.BodyError = "message.error.body"
				w.WriteHeader(http.StatusUnprocessableEntity)
			default:
				seq := posted.Add(1)
				response.PostedMessageID = &kernel.ID{byte(seq)}
				response.PostedTopicID, response.PostedEventSeq = topic, seq
			}
			templ.Handler(view.MessageComposer(response)).ServeHTTP(w, r)
		default:
			response := snapshot
			if r.URL.Query().Has("ordinary") {
				response.IdempotencyKey = nil
			}
			templ.Handler(view.ChannelScreen("/static/css/app.css", response)).ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(catalogues.Middleware(mux))
	defer server.Close()
	page, closePage := boundedPage(t, browser, ctx, "composer landscape keyboard")
	defer closePage()
	page = page.WithPanic(func(v any) { t.Fatal(v) })
	// iPhone 18 Pro landscape (874 × 402 pt) crosses desk (852 px) and short
	// (436 px) with a coarse primary pointer. In headless Chrome innerHeight is
	// 402 and visualViewport.offsetTop is 0; setting --composer-viewport-h on
	// #organization-stream stands in for iOS Safari's visualViewport.height
	// (90 px with one tab open, 40 px with four tabs and the tab bar showing).
	page.MustSetViewport(874, 402, 1, true)
	if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: true}).Call(page); err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(server.URL).MustWaitLoad()
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
	setBand := func(height int) {
		page.MustEval(`h => {
			const s = document.getElementById('organization-stream').style;
			if (h > 0) s.setProperty('--composer-viewport-h', h + 'px');
			else { s.removeProperty('--composer-viewport-h'); s.removeProperty('--composer-viewport-top'); }
		}`, height)
		settle()
	}
	set := func(text string) {
		page.MustEval(`text => { const b = document.getElementById('message-body'); b.value = text;
			b.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertFromPaste'})); }`, text)
		settle()
	}
	const reachable = `(selector, h) => {
		const el = document.querySelector(selector), c = document.getElementById('conversation');
		if (!el?.textContent.trim() || getComputedStyle(el).display === 'none' || getComputedStyle(el).visibility !== 'visible') return 'missing visible text: ' + selector;
		c.scrollTop += el.getBoundingClientRect().top;
		const top = el.getBoundingClientRect();
		if (top.top < 0 || top.top >= h || top.bottom <= 0) return 'top unreachable: ' + top.top + '..' + top.bottom;
		c.scrollTop += el.getBoundingClientRect().bottom - h;
		const bottom = el.getBoundingClientRect();
		return bottom.bottom <= h && bottom.bottom > 0 && bottom.top < h ? '' : 'bottom unreachable: ' + bottom.top + '..' + bottom.bottom;
	}`
	const newest = `() => { const l = document.getElementById('message-list'); return l.scrollHeight - l.scrollTop - l.clientHeight <= 2 ? '' : 'lost newest'; }`
	const stacked = `(rest, font) => {
		const form = document.getElementById('message-composer'), b = document.getElementById('message-body');
		const label = document.querySelector('label[for="message-body"]').getBoundingClientRect();
		const tr = b.getBoundingClientRect(), br = form.querySelector('button[type="submit"]:not([form])').getBoundingClientRect();
		if (getComputedStyle(form).flexDirection !== 'column') return 'not column';
		if (label.height < 16 || label.bottom > tr.top || tr.bottom > br.top) return 'not stacked';
		if (b.clientHeight !== rest) return 'resting height ' + b.clientHeight + ' want ' + rest;
		if (br.width !== 44 || br.height !== 44) return 'button ' + br.width + 'x' + br.height;
		return parseFloat(getComputedStyle(b).fontSize) === font ? '' : 'font ' + getComputedStyle(b).fontSize;
	}`
	coords := func(state string) {
		t.Log(page.MustEval(`s => { const tr = document.getElementById('message-body').getBoundingClientRect(), br = document.querySelector('#message-composer button[type="submit"]:not([form])').getBoundingClientRect();
			return s + ': textarea=' + tr.left + ',' + tr.top + '..' + tr.right + ',' + tr.bottom + ' (' + tr.width + 'x' + tr.height + ') button=' + br.left + ',' + br.top + '..' + br.right + ',' + br.bottom + ' (' + br.width + 'x' + br.height + ')'; }`, state).Str())
	}
	check("keyboard closed stacked layout", `() => innerWidth === 874 && innerHeight === 402 ? '' : 'viewport ' + innerWidth + 'x' + innerHeight`)
	check("keyboard closed geometry", stacked, 38, 16)
	coords("874x402 keyboard-closed")
	page.MustEval(`() => document.getElementById('message-body').focus()`)
	// 90 px stands for iOS Safari in landscape with one tab open: both the
	// focused textarea and the 44 × 44 send button must lie inside 0..90.
	setBand(90)
	const checkBand90 = `() => {
		const b = document.getElementById('message-body'), btn = document.querySelector('#message-composer button[type="submit"]:not([form])');
		const tr = b.getBoundingClientRect(), br = btn.getBoundingClientRect(), label = b.labels?.[0];
		if (document.activeElement !== b) return 'textarea not focused';
		if (!label || !label.textContent.trim()) return 'accessible label missing';
		if (parseFloat(getComputedStyle(b).fontSize) < 16) return 'font-size ' + getComputedStyle(b).fontSize;
		if (br.width !== 44 || br.height !== 44) return 'button size ' + br.width + 'x' + br.height;
		if (tr.top < 0 || tr.bottom > 90 || br.top < 0 || br.bottom > 90) return 'textarea ' + tr.top + '..' + tr.bottom + ', send button ' + br.top + '..' + br.bottom + ' outside 0..90';
		if (tr.right > br.left) return 'textarea not beside send button: textarea.right=' + tr.right + ' button.left=' + br.left;
		return '';
	}`
	check("90px one-tab band shows textarea and send button", checkBand90)
	coords("90px one-tab resting")
	tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: page.MustElement("#message-body").Object.ObjectID}).Call(page)
	if err != nil {
		t.Fatal(err)
	}
	label := page.MustEval(`() => document.getElementById('message-body').labels[0].textContent.trim()`).Str()
	if len(tree.Nodes) == 0 || tree.Nodes[0].Name == nil || tree.Nodes[0].Name.Value.Str() != label || label == "" {
		t.Fatalf("textarea accessible name must equal label %q: %+v", label, tree)
	}
	page.MustEval(`() => { const c = document.getElementById('conversation'); c.scrollTop -= 60; window.kbReaderTop = c.scrollTop; }`)
	foreign := int(posted.Add(1))
	message := streamTestMessages(topic, foreign)[0]
	message.MemberID, message.DisplayName, message.Handle = kernel.ID{9}, "Other member", "other"
	events <- realtime.Outgoing{ID: int64(foreign), Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(message))}
	page.MustWait(`seq => !!document.querySelector('#message-items > li[data-event-seq="' + seq + '"]')`, fmt.Sprint(foreign))
	check("foreign live message preserves keyboard-open reader", `() => Math.abs(document.getElementById('conversation').scrollTop - window.kbReaderTop) <= 1 ? '' : 'conversation moved from ' + window.kbReaderTop + ' to ' + document.getElementById('conversation').scrollTop`)

	// 40 px stands for iOS Safari in landscape with four tabs (tab bar
	// showing): the 38 px textarea lies inside 0..40, and the full 44 × 44
	// send button is reached by scrolling #conversation with the keyboard open.
	setBand(40)
	const checkBand40 = `() => {
		const b = document.getElementById('message-body'), btn = document.querySelector('#message-composer button[type="submit"]:not([form])');
		const tr = b.getBoundingClientRect(), br = btn.getBoundingClientRect();
		if (document.activeElement !== b) return 'textarea not focused';
		if (parseFloat(getComputedStyle(b).fontSize) < 16) return 'font-size ' + getComputedStyle(b).fontSize;
		if (br.width !== 44 || br.height !== 44) return 'button size ' + br.width + 'x' + br.height;
		return tr.top >= 0 && tr.bottom <= 40 ? '' : 'textarea ' + tr.top + '..' + tr.bottom + ' outside 0..40';
	}`
	check("40px four-tab band shows textarea", checkBand40)
	coords("40px four-tab resting")
	const reachButton = `() => {
		const c = document.getElementById('conversation'), b = document.getElementById('message-body');
		const btn = document.querySelector('#message-composer button[type="submit"]:not([form])');
		c.scrollTop = c.scrollHeight;
		const low = btn.getBoundingClientRect();
		if (low.width !== 44 || low.height !== 44 || low.bottom > 40 || low.bottom < 38) return 'button bottom not in 0..40: ' + low.top + '..' + low.bottom;
		c.scrollTop += low.top;
		const high = btn.getBoundingClientRect();
		window.kbButtonScroll = '40px button scroll: bottom=' + low.top + '..' + low.bottom + ', top=' + high.top + '..' + high.bottom;
		c.scrollTop = c.scrollHeight;
		if (document.activeElement !== b || document.getElementById('organization-stream').style.getPropertyValue('--composer-viewport-h') !== '40px') return 'keyboard state or focus lost';
		return high.width === 44 && high.height === 44 && Math.abs(high.top) <= 1 && high.bottom >= 40 ? '' : 'button top not reached in 0..40: ' + high.top + '..' + high.bottom;
	}`
	check("40px four-tab band reaches full 44x44 send button by scrolling #conversation", reachButton)
	t.Log(page.MustEval(`() => window.kbButtonScroll`).Str())
	page.MustSetViewport(844, 390, 1, true)
	settle()
	setBand(40)
	check("40px band at 844px width shows 38px textarea", checkBand40)
	coords("40px at 844x390 resting")
	check("40px band at 844px width reaches full 44x44 send button by scrolling", reachButton)
	t.Log(page.MustEval(`() => window.kbButtonScroll`).Str())
	page.MustSetViewport(874, 402, 1, true)
	settle()
	setBand(40)

	// Verify iOS Safari's visualViewport.offsetTop pan (238 at 90 px, 252 at 40 px).
	for _, pan := range [][2]int{{90, 238}, {40, 252}} {
		page.MustEval(`(h, top) => { const s = document.getElementById('organization-stream').style;
			s.setProperty('--composer-viewport-h', h + 'px'); s.setProperty('--composer-viewport-top', top + 'px'); }`, pan[0], pan[1])
		check(fmt.Sprintf("offsetTop=%d at %dpx band", pan[1], pan[0]), `(h, top) => {
			const tr = document.getElementById('message-body').getBoundingClientRect(), br = document.querySelector('#message-composer button[type="submit"]:not([form])').getBoundingClientRect();
			if (tr.top - top < 0 || tr.bottom - top > h) return 'textarea ' + (tr.top - top) + '..' + (tr.bottom - top) + ' outside 0..' + h;
			return h === 90 && (br.top - top < 0 || br.bottom - top > h) ? 'button ' + (br.top - top) + '..' + (br.bottom - top) + ' outside 0..90' : '';
		}`, pan[0], pan[1])
		coords(fmt.Sprintf("%dpx band offsetTop=%d (layout coordinates)", pan[0], pan[1]))
	}
	page.MustEval(`() => document.getElementById('organization-stream').style.removeProperty('--composer-viewport-top')`)

	// Multi-line growth, quote chip, and saved scroll position of #message-list.
	setBand(90)
	page.MustEval(`() => { const l = document.getElementById('message-list'); l.scrollTop = l.scrollHeight; }`)
	set("line1\nline2\nline3")
	check("three-line growth fits inside 90px band", `() => document.getElementById('message-body').clientHeight === 82 ? '' : 'not 82px'`)
	check("90px band holds 82px textarea and 44px button", checkBand90)
	coords("90px three-line growth")
	check("newest preserved on growth", newest)
	set(strings.Repeat("capped\n", 20) + "tail")
	check("inner scroll at cap keeps caret visible", `() => {
		const b = document.getElementById('message-body');
		b.focus(); b.setSelectionRange(b.value.length, b.value.length); b.scrollTop = b.scrollHeight;
		return b.clientHeight === 82 && b.scrollHeight > 82 && b.scrollHeight - b.scrollTop - b.clientHeight <= parseFloat(getComputedStyle(b).paddingBottom) + 1 ? '' : 'cap/scroll';
	}`)
	set("")
	page.MustEval(`() => document.getElementById('message-list').scrollTop = 500`)
	settle()
	anchor := page.MustEval(`() => { const l = document.getElementById('message-list'), top = l.getBoundingClientRect().top;
		const item = [...document.querySelectorAll('#message-items > li')].find(i => i.getBoundingClientRect().bottom > top);
		return {id: item.id, offset: item.getBoundingClientRect().top - top}; }`)
	const anchored = `a => Math.abs(document.getElementById(a.id).getBoundingClientRect().top - document.getElementById('message-list').getBoundingClientRect().top - a.offset) <= 1 ? '' : 'reader moved'`
	set("first\nsecond\nthird")
	check("reader preserved on growth in keyboard band", anchored, anchor)
	set("")
	check("reader preserved on shrink in keyboard band", anchored, anchor)
	for _, quoted := range []bool{true, false} {
		page.MustEval(`quoted => document.getElementById('quote-form').requestSubmit(document.querySelector(quoted ? '#message-items [name=quote]' : '#composer-quote button'))`, quoted)
		page.MustWait(`quoted => !!document.querySelector('#composer-quote input') === quoted && !document.querySelector('.htmx-settling')`, quoted)
		if quoted {
			page.MustEval(`() => document.getElementById('message-body').focus()`)
		}
		check(fmt.Sprintf("quote=%t keeps reader anchor", quoted), anchored, anchor)
		check(fmt.Sprintf("quote=%t keeps 90px band", quoted), checkBand90)
		if quoted {
			page.MustEval(`() => document.getElementById('quote-form').requestSubmit(document.querySelector('#composer-quote [name=part]'))`)
			page.MustWait(`() => document.activeElement?.id === 'quote-text' && !document.querySelector('.htmx-settling')`)
			check("quote edit focus reveals its textarea", `() => { const r = document.getElementById('quote-text').getBoundingClientRect(); return r.top >= 0 && r.bottom <= 90 ? '' : 'quote textarea ' + r.top + '..' + r.bottom; }`)
		}
	}
	check("quote removal focuses and reveals body", checkBand90)

	// IME composition: Enter during composition or within 100 ms of compositionend does not submit.
	page.MustEval(`() => {
		window.kbSubmits = 0;
		document.addEventListener('submit', e => { if (e.target.id === 'message-composer') window.kbSubmits++; }, true);
		const b = document.getElementById('message-body');
		b.focus();
		b.dispatchEvent(new CompositionEvent('compositionstart', {bubbles: true}));
		b.value = '日本語\n変換';
		b.dispatchEvent(new InputEvent('input', {bubbles: true, isComposing: true}));
		b.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true, isComposing: true, keyCode: 229}));
		b.dispatchEvent(new CompositionEvent('compositionend', {bubbles: true}));
		window.kbEndedAt = performance.now();
		b.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true}));
	}`)
	page.MustWait(`() => performance.now() - window.kbEndedAt >= 120`)
	check("IME Enter does not submit", `() => window.kbSubmits === 0 && document.getElementById('message-body').enterKeyHint === 'send' ? '' : 'submits=' + window.kbSubmits`)

	// 422 field error alert and post-send band recovery at 90 px and 40 px.
	set(strings.Repeat("x", 4005))
	page.MustEval(`() => { document.getElementById('message-body').focus(); document.getElementById('message-composer').requestSubmit(); }`)
	page.MustWait(`() => document.getElementById('message-body').getAttribute('aria-invalid') === 'true' && !document.querySelector('.htmx-settling')`)
	check("422 error alert in keyboard band", `() => document.querySelector('#message-help[role="alert"]')?.textContent.trim() ? '' : 'missing 422 alert'`)
	for _, height := range []int{90, 40} {
		setBand(height)
		check(fmt.Sprintf("422 alert reachable by scrolling at %dpx with capped textarea", height), reachable, "#message-help[role=alert]", height)
	}
	for _, height := range []int{90, 40} {
		for _, streamFirst := range []bool{false, true} {
			setBand(height)
			set(fmt.Sprintf("sent at %d", height))
			if streamFirst {
				holdNext <- struct{}{}
			}
			page.MustEval(`() => { document.getElementById('message-list').scrollTop = 0; document.getElementById('message-body').focus();
			document.getElementById('message-body').dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true, cancelable: true})); }`)
			seq := int(posted.Load()) + 1
			if !streamFirst {
				page.MustWait(`() => document.getElementById('message-body').value === '' && !!document.querySelector('#message-pending [data-state="confirmed"]') && !document.querySelector('.htmx-settling')`)
				seq = int(posted.Load())
			}
			events <- realtime.Outgoing{ID: int64(seq), Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, seq)[0]))}
			page.MustWait(`seq => !!document.querySelector('#message-items > li[data-event-seq="' + seq + '"]')`, fmt.Sprint(seq))
			if streamFirst {
				page.MustWait(`() => !!document.querySelector('#message-pending [data-state="sending"]')`)
				releaseInFlight <- struct{}{}
			}
			page.MustWait(`() => !document.querySelector('#message-pending > li') && !document.querySelector('.htmx-settling')`)
			check(fmt.Sprintf("newest after send at %dpx", height), newest)
			if height == 90 {
				check("composer back inside 90px band after send", checkBand90)
			} else {
				check("textarea back inside 40px band after send", checkBand40)
			}
			coords(fmt.Sprintf("%dpx after pending send (stream-first=%t)", height, streamFirst))
		}
	}

	// Ordinary send path: #message-send-alert on 500, #message-sending in flight, and 40 px band recovery.
	page.MustNavigate(server.URL + "?ordinary").MustWaitLoad()
	page.MustEval(`() => document.getElementById('message-body').focus()`)
	setBand(40)
	set("fail500")
	page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
	page.MustWait(`() => document.getElementById('message-send-alert').textContent.trim() !== '' && !document.querySelector('.htmx-settling')`)
	check("send alert populated on 500", `() => document.getElementById('message-send-alert').textContent.trim() ? '' : 'empty send alert'`)
	for _, height := range []int{90, 40} {
		setBand(height)
		set(strings.Repeat("capped\n", 20))
		check(fmt.Sprintf("500 alert reachable by scrolling at %dpx with capped textarea", height), reachable, "#message-send-alert", height)
	}
	holdNext <- struct{}{}
	set("ordinary at 40")
	page.MustEval(`() => { document.getElementById('message-list').scrollTop = 0; document.getElementById('message-body').focus(); document.getElementById('message-composer').requestSubmit(); }`)
	page.MustWait(`() => document.getElementById('message-composer').classList.contains('htmx-request')`)
	check("sending status visible in flight", `() => getComputedStyle(document.querySelector('#message-sending span')).display !== 'none' ? '' : 'sending hidden'`)
	for _, height := range []int{90, 40} {
		setBand(height)
		set(strings.Repeat("new draft\n", 20))
		check(fmt.Sprintf("sending status reachable by scrolling at %dpx with capped textarea", height), reachable, "#message-sending span", height)
	}
	set("ordinary at 40")
	releaseInFlight <- struct{}{}
	page.MustWait(`() => document.getElementById('message-body').value === '' && !document.querySelector('.htmx-settling')`)
	seq := int(posted.Load())
	events <- realtime.Outgoing{ID: int64(seq), Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, seq)[0]))}
	page.MustWait(`seq => document.getElementById('message-body').value === '' && !!document.querySelector('#message-items > li[data-event-seq="' + seq + '"]') && !document.querySelector('.htmx-settling')`, fmt.Sprint(seq))
	check("ordinary post newest at 40px", newest)
	check("ordinary post composer back inside 40px band", checkBand40)
	coords("40px after ordinary send and stream delivery")

	// Guard leaf: portrait phone (390 × 844, closed and keyboard open) and desktop (1280 × 800) are unchanged.
	for _, g := range []struct {
		name              string
		w, h, vh, rest, f int
		mobile            bool
	}{
		{"portrait-closed", 390, 844, 0, 44, 16, true},
		{"portrait-keyboard", 390, 844, 500, 44, 16, true},
		{"desktop", 1280, 800, 0, 38, 15, false},
	} {
		page.MustSetViewport(g.w, g.h, 1, g.mobile)
		if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: g.mobile}).Call(page); err != nil {
			t.Fatal(err)
		}
		setBand(g.vh)
		check("guard "+g.name, stacked, g.rest, g.f)
		coords("guard " + g.name)
	}
}
