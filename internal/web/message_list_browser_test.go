package web

import (
	"context"
	"encoding/json"
	"math"
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

// testMessageListWithoutJavaScript checks initial list positioning without
// scripts, preserving short and empty lists, history pages and permalinks.
func testMessageListWithoutJavaScript(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	topic, channel, cursor := kernel.ID{7}, kernel.ID{1}, int64(99)
	seqs := make([]int, 90)
	for i := range seqs {
		seqs[i] = i
	}
	latest := view.ChannelPage{
		Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:      view.Channel{ID: channel, Name: "general"},
		Topic:        &view.Topic{ID: topic, Name: "plans"},
		EventCursor:  &cursor, Reading: true, Older: true,
		Messages: streamTestMessages(topic, seqs[40:]...),
	}
	path := view.ConversationURL("acme", channel, &topic)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", idleStream)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		page := latest
		switch {
		case r.URL.Query().Has("before"):
			page.Before, page.Messages = 40, streamTestMessages(topic, seqs[5:40]...)
		case r.URL.Query().Has("around"):
			page.Around, page.Messages = 30, streamTestMessages(topic, seqs[5:55]...)
		case r.URL.Query().Get("fixture") == "few":
			page.Older, page.Messages = false, streamTestMessages(topic, 40, 41, 42)
		case r.URL.Query().Get("fixture") == "empty":
			page.Older, page.Messages = false, nil
		}
		templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Back must load a fresh document rather than restore a cached scroll.
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()

	type rect struct{ Top, Bottom, Left, Right float64 }
	type geometry struct {
		Scripting, TargetMatches, EmptyVisible                 bool
		Display, Active                                        string
		Pane, First, Last, Target, Heading, Empty              rect
		ScrollTop, PaddingTop, ConversationTop, ViewportHeight float64
	}
	inside := func(item, pane rect) bool {
		return item.Top >= pane.Top-1 && item.Bottom <= pane.Bottom+1 &&
			item.Left >= pane.Left-1 && item.Right <= pane.Right+1
	}
	const probe = `id => {
		const list = document.getElementById('message-list');
		const items = document.querySelectorAll('#message-items > li');
		const target = document.getElementById(id), empty = document.querySelector('#message-empty p');
		const box = e => {
			if (!e) return {};
			const r = e.getBoundingClientRect();
			return {top: r.top, bottom: r.bottom, left: r.left, right: r.right};
		};
		return {
			scripting: matchMedia('(scripting: none)').matches,
			display: getComputedStyle(list).display, pane: box(list),
			first: box(items[0]), last: box(items[items.length - 1]),
			target: box(target), targetMatches: !!target && target.matches(':target'),
			scrollTop: list.scrollTop,
			paddingTop: parseFloat(getComputedStyle(document.getElementById('message-items')).paddingTop),
			active: document.activeElement.tagName,
			conversationTop: document.getElementById('conversation').scrollTop,
			heading: box(document.querySelector('#conversation h1')), viewportHeight: innerHeight,
			empty: box(empty), emptyVisible: !!empty && empty.checkVisibility() &&
				empty.textContent.trim().length > 0 && empty.getBoundingClientRect().height > 0
		};
	}`
	for _, c := range []struct {
		name, kind, suffix string
		width, height      int
		scripts            bool
	}{
		{"width=1280/latest", "latest", "", 1280, 800, false},
		{"width=390/latest", "latest", "", 390, 844, false},
		{"height=400/latest", "latest", "", 1280, 400, false},
		{"width=1280/back", "back", "", 1280, 800, false},
		{"width=1280/few", "few", "?fixture=few", 1280, 800, false},
		{"width=1280/empty", "empty", "?fixture=empty", 1280, 800, false},
		{"width=1280/permalink-oldest", "permalink", "#" + view.MessageDOMID(kernel.ID{40}), 1280, 800, false},
		{"width=1280/before", "before", "?before=40&unread=0", 1280, 800, false},
		{"width=1280/around", "around", "?around=30", 1280, 800, false},
		{"scripts=true/width=1280/latest", "latest", "", 1280, 800, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatalf("%s: %v", c.name, v) })
			defer page.MustClose()
			page.MustSetViewport(c.width, c.height, 1, c.width == 390)
			if !c.scripts {
				if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
					t.Fatalf("%s: disabling scripts: %v", c.name, err)
				}
			}
			if err := navigateLoaded(page, server.URL+path+c.suffix); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			precondition := func() {
				t.Helper()
				scripting := page.MustEval(`() => matchMedia('(scripting: none)').matches`).Bool()
				if !c.scripts && !scripting {
					t.Fatalf("%s: scripting: none does not match (matches=%t); the leaf would pass vacuously", c.name, scripting)
				}
				if c.scripts && scripting {
					t.Fatalf("%s: scripting: none matches=%t, want false with scripts enabled", c.name, scripting)
				}
			}
			precondition()
			if c.kind == "back" {
				href := page.MustEval(`() => document.querySelector('#load-older a').href`).Str()
				if err := navigateLoaded(page, href); err != nil {
					t.Fatalf("%s: following Load older %q: %v", c.name, href, err)
				}
				// Browser history traversal avoids rod's page-script navigation
				// racing the replacement of the document's frame host (#1251).
				history, err := (proto.PageGetNavigationHistory{}).Call(page)
				if err != nil {
					t.Fatalf("%s: reading browser history: %v", c.name, err)
				}
				if history.CurrentIndex < 1 {
					t.Fatalf("%s: history index=%d entries=%d, want an earlier entry", c.name, history.CurrentIndex, len(history.Entries))
				}
				if err := (proto.PageNavigateToHistoryEntry{EntryID: history.Entries[history.CurrentIndex-1].ID}).Call(page); err != nil {
					t.Fatalf("%s: going Back: %v", c.name, err)
				}
				backCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				defer stop()
				var lastState string
				for {
					result, err := page.Context(backCtx).Eval(`() => ({search: location.search, readyState: document.readyState})`)
					if err == nil {
						lastState = result.Value.JSON("", "")
						if result.Value.Get("search").Str() == "" && result.Value.Get("readyState").Str() == "complete" {
							break
						}
					}
					// Evaluation can fail while Back replaces the document.
					select {
					case <-backCtx.Done():
						t.Fatalf("%s: Back did not load latest: state=%s eval error=%v timeout=%v", c.name, lastState, err, backCtx.Err())
					case <-time.After(30 * time.Millisecond):
					}
				}
				precondition()
			}
			measure := func() geometry {
				t.Helper()
				result := page.MustEval(probe, view.MessageDOMID(kernel.ID{40}))
				var got geometry
				if err := json.Unmarshal([]byte(result.JSON("", "")), &got); err != nil {
					t.Fatalf("%s: decoding geometry %s: %v", c.name, result.JSON("", ""), err)
				}
				return got
			}
			if c.scripts {
				page.MustActivate()
				page.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
			}
			got := measure()
			if c.scripts {
				for deadline := time.Now().Add(3 * time.Second); !inside(got.Last, got.Pane) && time.Now().Before(deadline); {
					time.Sleep(30 * time.Millisecond)
					got = measure()
				}
				if got.Display != "block" {
					t.Errorf("%s: list display=%q, want block; measurements=%+v", c.name, got.Display, got)
				}
			}
			switch c.kind {
			case "latest", "back":
				if !inside(got.Last, got.Pane) || inside(got.First, got.Pane) {
					t.Errorf("%s: newest entry not in view or first still in view: last=%+v first=%+v pane=%+v scrollTop=%g; measurements=%+v", c.name, got.Last, got.First, got.Pane, got.ScrollTop, got)
				}
				if !c.scripts && got.Active != "BODY" {
					t.Errorf("%s: activeElement=%q, want BODY; measurements=%+v", c.name, got.Active, got)
				}
				if c.height == 400 {
					if math.Abs(got.Pane.Bottom-got.Pane.Top-128) > 1 || got.ConversationTop != 0 || got.Heading.Top < -1 || got.Heading.Bottom > got.ViewportHeight+1 {
						t.Errorf("%s: short layout: pane height=%g want 128, conversation scrollTop=%g want 0, h1=%+v viewport height=%g; measurements=%+v", c.name, got.Pane.Bottom-got.Pane.Top, got.ConversationTop, got.Heading, got.ViewportHeight, got)
					}
				}
			case "few":
				if math.Abs(got.First.Top-got.Pane.Top-got.PaddingTop) > 1 {
					t.Errorf("%s: first top=%g, want pane top=%g + padding top=%g; measurements=%+v", c.name, got.First.Top, got.Pane.Top, got.PaddingTop, got)
				}
			case "empty":
				if !got.EmptyVisible || !inside(got.Empty, got.Pane) || math.Abs((got.Empty.Top+got.Empty.Bottom-got.Pane.Top-got.Pane.Bottom)/2) > 1 {
					t.Errorf("%s: empty text visible=%t rect=%+v, want centred in pane=%+v; measurements=%+v", c.name, got.EmptyVisible, got.Empty, got.Pane, got)
				}
			case "permalink":
				if !inside(got.Target, got.Pane) || !got.TargetMatches || inside(got.Last, got.Pane) {
					t.Errorf("%s: target=%+v :target=%t newest=%+v pane=%+v; measurements=%+v", c.name, got.Target, got.TargetMatches, got.Last, got.Pane, got)
				}
			case "before", "around":
				if !inside(got.First, got.Pane) || got.ScrollTop != 0 {
					t.Errorf("%s: first=%+v pane=%+v scrollTop=%g, want first in view and scrollTop 0; measurements=%+v", c.name, got.First, got.Pane, got.ScrollTop, got)
				}
			}
		})
	}
}
