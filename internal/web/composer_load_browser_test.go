package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testComposerLoadBrowser(t *testing.T, browser *rod.Browser) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int, 50)
	for i := range seqs {
		seqs[i] = i + 1
	}
	snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current: view.Channel{ID: kernel.ID{1}, Name: "general"}, EventCursor: new(int64(50)),
		Messages: streamTestMessages(kernel.ID{2}, seqs...)}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", idleStream)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		response := snapshot
		if r.URL.Query().Has("quote") {
			response.Quote = &snapshot.Messages[0]
		}
		if r.URL.Query().Has("capped") {
			response.Body = strings.Repeat("preset\n", 100)
		}
		templ.Handler(view.ChannelScreen("/static/css/app.css", response)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(catalogues.Middleware(mux))
	defer server.Close()
	for _, tc := range []struct {
		name, query string
		width       int
	}{
		{"desktop", "", 1280},
		{"narrow", "", 390},
		{"quote", "?quote", 390},
		{"quote-capped", "?quote&capped", 390},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			tab, closeTab := boundedPage(t, browser, ctx, "composer load")
			defer closeTab()
			steps := newStepRunner(t, ctx, 10*time.Second)
			base := tab.WithPanic(steps.fail)
			page := base
			step := func(what string) { page = base.Context(steps.next(what)) }
			settle := func() {
				page.MustEval(`() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
			}
			check := func(what, js string, args ...any) {
				t.Helper()
				step(what)
				settle()
				if problem := page.MustEval(js, args...).Str(); problem != "" {
					t.Fatalf("%s: %s", what, problem)
				}
			}
			step("install early settle and first-paint recorder")
			page.MustEvalOnNewDocument(`(() => {
				window.composerFrames = [];
				const deadline = performance.now() + 5000;
				// A task after each rAF samples the observer result for that paint.
				const record = () => requestAnimationFrame(() => setTimeout(() => {
					const l = document.getElementById('message-list'), items = document.getElementById('message-items');
					if (l && items?.lastElementChild) {
						// Include the reading column's existing bottom gutter.
						const bottom = items.lastElementChild.getBoundingClientRect().bottom + parseFloat(getComputedStyle(items).paddingBottom);
						window.composerFrames.push({top: l.scrollTop, gap: l.getBoundingClientRect().bottom - bottom});
					}
					if (window.composerFrames.length < 8 && performance.now() < deadline) record();
				}, 0));
				// Deferred scripts have all run at DOMContentLoaded. A paint before
				// them shows the unscrolled list in every composer version, so the
				// recorder starts here, at the first rendering opportunity after the
				// composer script is evaluated.
				document.addEventListener('DOMContentLoaded', () => {
					// A stream settle in a background tab can precede measurement.
					const item = document.getElementById('message-items').lastElementChild;
					item.dispatchEvent(new CustomEvent('htmx:afterSettle', {bubbles: true, detail: {target: item}}));
					record();
				}, {once: true});
			})()`)
			step("set load viewport")
			page.MustSetViewport(tc.width, 600, 1, false)
			step("load 50-message feed")
			page.MustNavigate(server.URL + tc.query).MustWaitLoad()
			step("wait for first measurements and eight painted frames")
			page.MustWait(`() => window.composerFrames.length === 8`)
			const anchored = `() => {
				const l = document.getElementById('message-list'), frames = window.composerFrames;
				if (document.querySelectorAll('#message-items > li').length !== 50 || l.scrollTop <= 0) return 'feed not scrollable or not 50 messages';
				if (Math.abs(l.scrollHeight - l.clientHeight - l.scrollTop) > 1) return 'not at newest';
				if (frames.some(f => Math.abs(f.gap) > 1)) return 'newest bottom moved: ' + JSON.stringify(frames);
				if (frames.some(f => f.top !== l.scrollTop)) return 'scrollTop changed after first paint: ' + JSON.stringify(frames);
				return '';
			}`
			check("first paint and settled newest anchor", anchored)
			deduction := 353
			if tc.width < 852 { // desk breakpoint: --breakpoint-desk in web/styles/app.css.
				deduction = 337
			}
			check("viewport fallback and initial quote cap", `(quoted, deduction) => {
				const frame = document.getElementById('organization-stream'), b = document.getElementById('message-body');
				if (document.documentElement.style.length !== 0) return 'html has inline styles';
				if (frame.style.getPropertyValue('--composer-viewport-h') || frame.style.getPropertyValue('--composer-viewport-top')) return 'desktop/equal viewport override';
				const q = document.getElementById('composer-quote').getBoundingClientRect().height;
				const extra = q ? q + parseFloat(getComputedStyle(b.form).rowGap) : 0;
				if (quoted && !(Math.abs(parseFloat(b.form.style.getPropertyValue('--composer-quote-h')) - extra) <= 0.01)) return 'quote deduction incorrect';
				if (quoted && !b.form.style.getPropertyValue('--composer-quote-h')) return 'quote was not measured';
				const cap = Math.max(82, Math.min(innerHeight / 2, innerHeight - deduction - extra));
				if (!(Math.abs(parseFloat(getComputedStyle(b).maxHeight) - cap) <= 0.01)) return 'dvh/quote cap incorrect';
				if (b.value && (b.clientHeight !== Math.floor(cap) || b.scrollHeight <= b.clientHeight)) return 'preset did not reach quote-aware cap';
				return '';
			}`, tc.query != "", deduction)
			check("newest remains anchored after cap check", anchored)
		})
	}
}
