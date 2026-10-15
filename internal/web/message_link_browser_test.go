package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// A message's URL lands on the message (decision 36): with and without
// JavaScript, at desktop and phone width, an off-bottom target on a latest
// page and on an around page is in view and highlighted in light and dark.
func testMessageLinkBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	topic, cursor := kernel.ID{7}, int64(99)
	seqs := make([]int, 90)
	for i := range seqs {
		seqs[i] = i
	}
	latest := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}, Name: "general"},
		Topic: &view.Topic{ID: topic, Name: "plans"}, EventCursor: &cursor, Reading: true, Messages: streamTestMessages(topic, seqs[40:]...)}
	around := latest
	around.Around, around.Older, around.Messages = 30, true, streamTestMessages(topic, seqs[5:55]...)
	pages := map[string]view.ChannelPage{"/latest": latest, "/around": around}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", idleStream)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The latest page reads what it shows.
		if page, ok := pages[r.URL.Path]; ok && r.Method == http.MethodGet {
			templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, scripts := range []bool{true, false} {
		page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		if !scripts {
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
				t.Fatal(err)
			}
		} else {
			// Chromium scrolls to the fragment after deferred scripts, which
			// hides an initial scroll to the newest entry; count the writes.
			page.MustEvalOnNewDocument(`(() => {
				const scrollTop = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
				window.listWrites = 0;
				Object.defineProperty(Element.prototype, 'scrollTop', {...scrollTop, set(v) {
					if (this.id === 'message-list') window.listWrites++;
					scrollTop.set.call(this, v);
				}});
			})()`)
		}
		for _, size := range [][2]int{{1280, 800}, {375, 812}} {
			page.MustSetViewport(size[0], size[1], 1, size[0] < 768)
			for _, c := range []struct {
				path string
				seq  byte
			}{{"/latest", 50}, {"/around", 30}} {
				what := fmt.Sprintf("scripts=%t width=%d %s", scripts, size[0], c.path)
				target := view.MessageDOMID(kernel.ID{c.seq})
				if err := navigateLoaded(page, server.URL+c.path+"#"+target); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				// Animation frames never run with scripts disabled.
				if scripts {
					page.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
					if writes := page.MustEval(`() => window.listWrites`).Int(); writes != 0 {
						t.Errorf("%s: the list was scrolled %d times on load, want none", what, writes)
					}
				}
				for _, scheme := range []struct{ name, fill string }{{"light", "rgb(213, 241, 234)"}, {"dark", "rgb(29, 61, 56)"}} {
					if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: scheme.name}}}).Call(page); err != nil {
						t.Fatal(err)
					}
					got := page.MustEval(`id => {
						const list = document.getElementById('message-list'), item = document.getElementById(id);
						const port = list.getBoundingClientRect(), box = item.getBoundingClientRect();
						const shown = box.top >= port.top && box.bottom <= port.bottom && list.scrollTop + list.clientHeight < list.scrollHeight - 1;
						return [shown, item.matches(':target'), getComputedStyle(item).backgroundColor].join();
					}`, target).Str()
					if want := "true,true," + scheme.fill; got != want {
						t.Errorf("%s %s: shown, :target, fill = %s; want %s", what, scheme.name, got, want)
					}
				}
			}
		}
		page.MustClose()
	}
	// Without a fragment the same page starts at the newest entry.
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	if err := navigateLoaded(page, server.URL+"/around"); err != nil {
		t.Fatal(err)
	}
	if !page.MustEval(`() => { const list = document.getElementById('message-list'); return list.scrollTop + list.clientHeight >= list.scrollHeight - 1; }`).Bool() {
		t.Error("a page without a fragment does not start at the newest entry")
	}
}
