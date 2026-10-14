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

// On a phone's touch screen, in light and dark, a tapped row or button shows
// its press and keeps no highlight afterwards, and only the selection pill on
// the list page and the panel's sheet move, visible from their first frame;
// under reduced motion nothing moves (#975).
func testMotionTouchBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cursor := int64(42)
	general, random := view.Channel{ID: kernel.ID{1}, Name: "general"}, view.Channel{ID: kernel.ID{2}, Name: "random"}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("GET /organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if sender.Heartbeat(r.Context()) == nil {
			<-r.Context().Done()
		}
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		page := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Current: general,
			Channels: []view.Channel{general, random}, Handle: "alice", Role: "owner", EventCursor: &cursor}
		screen := view.ChannelScreen
		if r.URL.Path == view.ChannelURL("acme", general.ID)+"/"+view.PanelMembers {
			page.Panel, page.Members = view.PanelMembers, &view.MemberPage{}
		} else if r.URL.Path == "/organizations/acme/channels" {
			page.List, screen = true, view.ChannelListScreen
		}
		templ.Handler(screen("/static/css/app.css", page)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	// Ends the page's stream even if its tab failed to close, so server.Close
	// cannot wait on it.
	defer server.CloseClientConnections()
	// stage names what runs, for failures that reach the panic handler.
	stage := "opening the tab"
	tab := browser.Context(ctx).MustPage()
	// Cleanup gets its own bound, so it works after ctx has expired and a
	// stalled close cannot hang the test.
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := tab.Context(closeCtx).Close(); err != nil {
			t.Errorf("closing the tab: %v", err)
		}
	}()
	page := tab.Context(ctx).WithPanic(func(v any) { t.Fatalf("%s: %v", stage, v) })
	// Animations run, and their events fire, only in a tab that repaints.
	page.MustActivate()
	page.MustSetViewport(390, 844, 1, true)
	// Touch emulation, not media emulation (Chrome ignores hover and pointer
	// there), makes the page a touch screen without hover.
	if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: true}).Call(page); err != nil {
		t.Fatal(err)
	}
	// Every animation and transition that starts, with the properties its
	// first keyframe sets and any delay.
	page.MustEvalOnNewDocument(`window.motion = [];
		window.blockClicks = false;
		document.addEventListener('click', e => { if (blockClicks) e.preventDefault(); }, true);
		for (const type of ['animationstart', 'transitionrun']) document.addEventListener(type, e => {
			// Record the event before reading its effect, so a missing effect is
			// reported instead of throwing in the listener.
			const name = e.animationName || e.propertyName, at = motion.push(name) - 1;
			const effect = e.target.getAnimations().find(a => (a.animationName || a.transitionProperty) === name)?.effect;
			if (!effect) return void (motion[at] = name + ' (no effect)');
			const from = e.animationName ? Object.entries(effect.getKeyframes()[0]).filter(([k]) => !['offset', 'computedOffset', 'easing', 'composite'].includes(k)).map(([k, v]) => ' ' + k + ': ' + v).join(';') : '';
			const delay = effect.getTiming().delay ? ' delay ' + effect.getTiming().delay : '';
			motion[at] = name + from + delay;
		}, true);`)
	// expect reports, then forgets, the motion started so far.
	expect := func(step, want string) {
		t.Helper()
		page.MustEval(`() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)))`)
		if got := page.MustEval(`() => { const got = motion.join(', '); motion.length = 0; return got; }`).Str(); got != want {
			t.Fatalf("%s: motion = %q, want %q", step, got, want)
		}
	}
	centre := func(selector string) (x, y float64) {
		var at struct{ X, Y float64 }
		if err := page.MustEval(`s => { const r = document.querySelector(s).getBoundingClientRect(); return {x: r.x + r.width / 2, y: r.y + r.height / 2}; }`, selector).Unmarshal(&at); err != nil {
			t.Fatal(err)
		}
		return at.X, at.Y
	}
	// touchTap taps through rod's Touch, which dispatches on the original tab
	// outside ctx, so it runs under its own bound and fails by name (#1060).
	touchTap := func(what string, x, y float64) {
		t.Helper()
		done := make(chan any, 1)
		go func() {
			defer func() { done <- recover() }()
			tab.Touch.MustTap(x, y)
		}()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case v := <-done:
			if v != nil {
				t.Fatalf("tap on %s: %v", what, v)
			}
		case <-timer.C:
			t.Fatalf("tap on %s did not finish within 10 s", what)
		case <-ctx.Done():
			t.Fatalf("tap on %s: the test's time ran out", what)
		}
	}
	// navigate taps the selector and waits for the page it leads to.
	navigate := func(selector string) {
		t.Helper()
		page.MustEval(`() => { blockClicks = false; window.leaving = true; }`)
		x, y := centre(selector)
		touchTap(selector, x, y)
		if err := page.Timeout(10 * time.Second).Wait(rod.Eval(`() => !window.leaving && document.readyState === 'complete'`)); err != nil {
			t.Fatalf("tap on %s led to no loaded page: %v", selector, err)
		}
	}
	const look = `s => { const c = getComputedStyle(document.querySelector(s)); return [c.backgroundColor, c.color, c.outlineStyle, c.scale].join(' / '); }`
	// tap taps the selector with its click blocked, so the page stays: it
	// must scale down while :active, unless motion is reduced, and end at its
	// resting look although the tap leaves it :hover, as on a phone.
	tap := func(where, selector string, reduce bool) {
		t.Helper()
		page.MustEval(`() => { blockClicks = true; }`)
		rest := page.MustEval(look, selector).Str()
		page.MustEval(`s => { const e = document.querySelector(s), start = performance.now();
			window.tapped = new Promise(done => {
				let active = false, pressed = 1;
				const tick = () => {
					const scale = getComputedStyle(e).scale, now = e.matches(':active');
					if (now && scale !== 'none') pressed = Math.min(pressed, parseFloat(scale));
					active ||= now;
					if (active && !now && scale === 'none' || performance.now() - start > 3000) return done({active, pressed});
					requestAnimationFrame(tick);
				};
				tick();
			}); }`, selector)
		x, y := centre(selector)
		touchTap(selector, x, y)
		stage = where + ": awaiting the tap on " + selector
		var press struct {
			Active  bool
			Pressed float64
		}
		if err := page.MustEval(`() => tapped`).Unmarshal(&press); err != nil {
			t.Fatal(err)
		}
		switch {
		case !press.Active:
			t.Fatalf("%s: tapping %s never made it :active", where, selector)
		case reduce && press.Pressed != 1:
			t.Fatalf("%s: %s scaled to %v under reduced motion", where, selector, press.Pressed)
		case !reduce && press.Pressed >= 1:
			t.Fatalf("%s: %s showed no press feedback while :active", where, selector)
		case !page.MustEval(`s => document.querySelector(s).matches(':hover')`, selector).Bool():
			t.Fatalf("%s: the tap left %s without :hover, so this check proves nothing", where, selector)
		}
		if after := page.MustEval(look, selector).Str(); after != rest {
			t.Fatalf("%s: %s stays highlighted after the tap: %s, at rest %s", where, selector, after, rest)
		}
		want := "scale, scale"
		if reduce {
			want = ""
		}
		stage = where + ": the motion after tapping " + selector
		expect(where+": tapping "+selector, want)
	}
	link := func(id kernel.ID) string { return "#link-" + view.ChannelDOMID(id) }
	for _, pass := range []struct {
		scheme string
		reduce bool
	}{{"light", false}, {"dark", false}, {"light", true}} {
		where := fmt.Sprintf("390 px, %s, reduce=%t", pass.scheme, pass.reduce)
		motion, pill, sheet := "no-preference", "rb-pill-in transform: scaleY(0)", "rb-panel-in transform: translateY(16px)"
		if pass.reduce {
			motion, pill, sheet = "reduce", "", ""
		}
		media := proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: pass.scheme}, {Name: "prefers-reduced-motion", Value: motion}}}
		if err := media.Call(page); err != nil {
			t.Fatal(err)
		}
		stage = where + ": loading the conversation"
		if err := page.Timeout(10 * time.Second).Navigate(server.URL + view.ChannelURL("acme", general.ID)); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if err := page.Timeout(10 * time.Second).WaitLoad(); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if got := page.MustEval(`() => [matchMedia('(hover: hover)').matches, matchMedia('(pointer: coarse)').matches, matchMedia('(prefers-color-scheme: dark)').matches].join()`).Str(); got != fmt.Sprintf("false,true,%t", pass.scheme == "dark") {
			t.Fatalf("%s: hover, coarse pointer, dark = %s", where, got)
		}
		expect(where+": first load", "")
		tap(where, "#panel-control-members", pass.reduce)
		tap(where, "#message-composer button[type=submit]", pass.reduce)
		// The back link leads to the list page, whose pill grows in; the
		// page itself does not move.
		navigate(`#conversation header a[href*="channels?channel="]`)
		expect(where+": the list page", pill)
		tap(where, link(random.ID), pass.reduce)
		tap(where, `nav:has(> a[aria-current="page"]) > button`, pass.reduce)
		// A conversation hides the sidebar, so its pill does not move.
		navigate(link(general.ID))
		expect(where+": the conversation from the list", "")
		navigate("#panel-control-members")
		expect(where+": the panel's sheet", sheet)
	}
}
