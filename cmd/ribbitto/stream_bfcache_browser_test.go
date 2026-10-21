package main

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func TestStreamBFCacheBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	// This helper deliberately does not disable the back/forward cache.
	browser := launchBrowser(t, ctx, "HTTP/1.1 stream bfcache acceptance")
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("block-pagehide=%t", blocked), func(t *testing.T) {
			testStreamBFCache(t, ctx, browser, blocked)
		})
	}
}

func testStreamBFCache(t *testing.T, ctx context.Context, browser *rod.Browser, blocked bool) {
	t.Helper()
	type request struct{ channel, after, lastID string }
	requests := make(chan request, 64)
	var active, peak, total atomic.Int32
	channels := make([]view.Channel, 9)
	for i := range channels {
		channels[i] = view.Channel{ID: kernel.ID{byte(i + 1)}, Name: fmt.Sprintf("channel-%d", i)}
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("protocol = %s, want HTTP/1.1", r.Proto)
		}
		// Mirror page/session and production stream headers. Only the wrapper
		// below removes them; internal/web/stream.go must retain no-store.
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/organizations/acme/events" {
			for _, channel := range channels {
				if r.URL.Path == view.ChannelURL("acme", channel.ID) {
					snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
						Current: channel, Channels: channels, EventCursor: new(int64(42))}
					templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		total.Add(1)
		requests <- request{r.URL.Query().Get("channel"), r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")}
		w.Header().Set("Content-Type", "text/event-stream")
		// Every connection replays 51. Restores also deliver 52, proving the
		// extension rebound its sse-swap children and replay stayed deduplicated.
		for _, seq := range []int64{51, 52} {
			if seq == 52 && r.URL.Query().Get("after") == "42" {
				break
			}
			var body bytes.Buffer
			message := view.Message{ID: kernel.ID{byte(seq)}, EventSeq: seq, Body: fmt.Sprint(seq), Text: fmt.Sprint(seq)}
			if err := view.LiveMessageItem(message).Render(r.Context(), &body); err != nil {
				t.Error(err)
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", seq, strings.ReplaceAll(body.String(), "\n", "\ndata: ")); err != nil {
				return
			}
		}
		if err := http.NewResponseController(w).Flush(); err == nil {
			<-r.Context().Done()
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(bfcacheCacheWriter{w}, r)
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	// The negative control runs the same assertions with the pagehide handling
	// suppressed and must trip one of them. Whether Chrome then keeps the
	// page's stream (bfcache restore without reopen) or refuses bfcache, the
	// acceptance assertions below fail; fail records that instead of ending
	// the test, and a run that passes is the real failure.
	fail := func(format string, args ...any) {
		t.Helper()
		if blocked {
			panic(controlFailure(fmt.Sprintf(format, args...)))
		}
		t.Fatalf(format, args...)
	}
	if blocked {
		defer func() {
			switch r := recover().(type) {
			case nil:
				t.Fatal("negative control passed: removing the pagehide handling went undetected")
			case controlFailure:
				t.Logf("negative control tripped as required: %s", string(r))
			default:
				panic(r)
			}
		}()
	}
	page, closePage := boundedPage(t, browser, ctx, "bfcache")
	defer closePage()
	page.MustActivate()
	if err := (proto.EmulationSetDeviceMetricsOverride{Width: 390, Height: 844, DeviceScaleFactor: 1, Mobile: true}).Call(page); err != nil {
		t.Fatal(err)
	}
	if err := (proto.EmulationSetCPUThrottlingRate{Rate: 4}).Call(page); err != nil {
		t.Fatal(err)
	}
	page.MustEvalOnNewDocument(`window.sources = []; window.shown = false; window.restored = false;
		window.EventSource = class extends EventSource {
			constructor(...args) { super(...args); window.sources.push(this); }
		};
		window.addEventListener('pagehide', () => window.shown = false, true);
		window.addEventListener('pageshow', e => { window.shown = true; window.restored = e.persisted; });`)
	if blocked {
		// In-process mutation: prevent the application's pagehide handler from
		// running, without editing an asset or weakening the positive test.
		page.MustEvalOnNewDocument(`window.addEventListener('pagehide', e => e.stopImmediatePropagation(), true)`)
	}
	cursors := make([]string, len(channels))
	opens := make([]int, len(channels))
	var expected int32
	// Five seconds includes navigation, the extension's 500 ms retry, DOM
	// application, and a one-second observation window for a second stream.
	navigate := func(index, delta int) {
		t.Helper()
		stepCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		p := page.Context(stepCtx).WithPanic(func(v any) { fail("channel %d history %d (5 s deadline): %v", index, delta, v) })
		path := view.ChannelURL("acme", channels[index].ID)
		if delta == 0 {
			p.MustNavigate(server.URL + path)
		} else {
			history, err := proto.PageGetNavigationHistory{}.Call(p)
			if err != nil {
				fail("%v", err)
			}
			if err := (proto.PageNavigateToHistoryEntry{EntryID: history.Entries[history.CurrentIndex+delta].ID}).Call(p); err != nil {
				fail("%v", err)
			}
		}
		p.MustWait(`path => location.pathname === path && window.shown && document.readyState === 'complete'`, path)
		if delta != 0 && !p.MustEval(`() => window.restored`).Bool() {
			fail("no real pageshow.persisted restore for %s; notRestoredReasons: %s", path,
				p.MustEval(`() => JSON.stringify(performance.getEntriesByType('navigation')[0]?.notRestoredReasons)`).Str())
		}
		{
			want := cursors[index]
			if want == "" {
				want = "42"
			}
			select {
			case got := <-requests:
				channel := strings.TrimPrefix(path, "/organizations/acme/channels/")
				if got != (request{channel, want, ""}) {
					fail("stream request = %+v, want channel=%s after=%s and no Last-Event-ID", got, channel, want)
				}
			case <-stepCtx.Done():
				fail("restore did not reopen exactly once: %v", stepCtx.Err())
			}
			expected++
			opens[index]++
			cursors[index] = "51"
			if delta != 0 {
				cursors[index] = "52"
			}
		}
		p.MustWait(`(cursor, count) => document.getElementById('organization-stream').dataset.eventCursor === cursor &&
			window.sources.length === count && window.sources.filter(s => s.readyState === 1).length === 1`, cursors[index], opens[index])
		p.MustEval(`() => new Promise(done => setTimeout(done, 1000))`)
		if total.Load() != expected || !p.MustEval(`count => window.sources.length === count && window.sources.filter(s => s.readyState !== 2).length === 1`, opens[index]).Bool() {
			fail("second stream in tab: requests=%d want=%d", total.Load(), expected)
		}
		if peak.Load() >= 6 {
			fail("concurrent HTTP/1.1 streams reached %d (six stalls navigation)", peak.Load())
		}
		if active.Load() != 1 {
			fail("active streams=%d want=1; a cached page kept its stream (pagehide release regression)", active.Load())
		}
		if !p.MustEval(`() => document.querySelectorAll('#message-items > li').length ===
			(document.getElementById('organization-stream').dataset.eventCursor === '52' ? 2 : 1)`).Bool() {
			fail("replay duplicated or lost a message")
		}
	}
	navigate(0, 0)
	for i := 1; i <= 8; i++ {
		navigate(i, 0)
		navigate(i-1, -1)
		navigate(i, 1)
	}
	t.Logf("8 phone navigations and 16 persisted restores within 5 s each; peak concurrent SSE requests=%d", peak.Load())
}

// controlFailure marks an expected failure of the negative control.
type controlFailure string

// bfcacheCacheWriter removes no-store only in this HTTP/1.1 fixture, including
// from pages: session middleware also uses it. Production headers stay intact.
type bfcacheCacheWriter struct{ http.ResponseWriter }

func (w bfcacheCacheWriter) WriteHeader(status int) {
	w.Header().Del("Cache-Control")
	w.ResponseWriter.WriteHeader(status)
}

func (w bfcacheCacheWriter) Write(p []byte) (int, error) {
	w.Header().Del("Cache-Control")
	return w.ResponseWriter.Write(p)
}

func (w bfcacheCacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
