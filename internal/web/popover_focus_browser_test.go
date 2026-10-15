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
	"github.com/go-rod/rod/lib/input"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Tab and Shift-Tab never leave focus outside an open popover, which could
// cover the focused control (#1092): leaving one closes it, and focus stays
// where Tab moved it. Escape still closes it with focus back on its button,
// and an outside click closes it with focus where the click put it. Opening
// the account menu with Enter or Space moves focus into it (#1102).
func testPopoverFocusBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	const css = "/static/css/app.css"
	conversation := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:     view.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: "general"},
		DisplayName: "Reader", Handle: "reader", Role: "member", ImageKeys: []string{"lake", "flowers", "moon", "kite"}}
	for i := range 20 {
		conversation.Channels = append(conversation.Channels, view.Channel{ID: kernel.ID{byte(i + 1)}, Name: fmt.Sprintf("room%02d", i+1)})
	}
	list := conversation
	list.List = true
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.Handle("GET /conversation", templ.Handler(view.ChannelScreen(css, conversation)))
	mux.Handle("GET /list", templ.Handler(view.ChannelListScreen(css, list)))
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "popover focus")
	defer closeTab()

	started := time.Now()
	since := func() time.Duration { return time.Since(started).Round(time.Millisecond) }
	current, stepCancel := "setup", context.CancelFunc(func() {})
	defer func() { stepCancel() }()
	base := tab.WithPanic(func(v any) { t.Fatalf("%s: %v (at %s)", current, v, since()) })
	page := base
	step := func(what string) {
		stepCancel()
		var stepCtx context.Context
		stepCtx, stepCancel = context.WithTimeout(ctx, 20*time.Second)
		current, page = what, base.Context(stepCtx)
	}
	// device runs mouse and keyboard work, which rod dispatches through the
	// original page outside any step context, under the step's deadline.
	device := func(what string, fn func(p *rod.Page)) {
		t.Helper()
		p := page.WithPanic(func(v any) { panic(v) })
		done := make(chan any, 1)
		go func() {
			defer func() { done <- recover() }()
			fn(p)
		}()
		select {
		case v := <-done:
			if v != nil {
				t.Fatalf("%s: %s: %v (at %s)", current, what, v, since())
			}
		case <-page.GetContext().Done():
			t.Fatalf("%s: %s did not finish (at %s)", current, what, since())
		}
	}
	tabKey := func(what string, shift bool) {
		t.Helper()
		device(what, func(p *rod.Page) {
			if shift {
				p.KeyActions().Press(input.ShiftLeft).Type(input.Tab).MustDo()
			} else {
				p.Keyboard.MustType(input.Tab)
			}
		})
	}
	// focused names the focused element, for failure messages.
	focused := func() string {
		return page.MustEval(`() => document.activeElement.outerHTML.slice(0, 100)`).Str()
	}
	// check evaluates js once, a timer turn after the input: the script
	// closes a popover in a timer after Tab, so a poll could pass on the
	// state before it, and a timer left from one key could act after the next.
	check := func(what, js string, args ...any) {
		t.Helper()
		if !page.MustEval(`async (...args) => { await new Promise(r => setTimeout(r)); return (`+js+`)(...args); }`, args...).Bool() {
			t.Fatalf("%s: %s, with focus on %s (at %s)", current, what, focused(), since())
		}
	}
	// The visible one of a popover's invokers: the account menu has two.
	const invoker = `id => [...document.querySelectorAll('[popovertarget="' + id + '"]')].find(e => e.checkVisibility())`
	const isOpen = `id => document.getElementById(id).matches(':popover-open')`
	const inside = `id => document.getElementById(id).matches(':popover-open') && document.getElementById(id).contains(document.activeElement)`
	// closeAndFocus starts from the closed popover with focus on its button;
	// Shift-Tab may have left it open, which the rule allows.
	const closeAndFocus = `id => { const p = document.getElementById(id); if (p.matches(':popover-open')) p.hidePopover(); (` + invoker + `)(id).focus(); }`
	const onInvoker = `id => document.activeElement === (` + invoker + `)(id)`
	// settled is the rule: focus is inside the open popover, or it is closed.
	const settled = `id => !document.getElementById(id).matches(':popover-open') || document.getElementById(id).contains(document.activeElement)`

	for _, c := range []struct {
		name, path    string
		width, height int
		popover       string
		// outside is a control the popover does not cover, and landed the
		// element a click on it focuses.
		outside, landed string
	}{
		{"desktop/create", "/conversation", 1440, 900, "channel-create", "#message-body", "#message-body"},
		{"desktop/account", "/conversation", 1440, 900, "account-menu", "#message-body", "#message-body"},
		{"phone/create", "/list", 390, 844, "channel-create", "main h1", "body"},
		{"phone/account", "/list", 390, 844, "account-menu", "main h1", "body"},
	} {
		open := func() {
			t.Helper()
			page.MustEval(closeAndFocus, c.popover)
			device("Enter on the button", func(p *rod.Page) { p.Keyboard.MustType(input.Enter) })
			check("the popover opens", isOpen, c.popover)
			tabKey("Tab into the popover", false)
			check("Tab enters the popover", inside, c.popover)
		}

		step(c.name + ": loading")
		// A background tab would delay timers by up to a second.
		page.MustActivate().MustSetViewport(c.width, c.height, 1, false)
		page.MustNavigate(server.URL + c.path).MustWaitLoad()

		// Enter can open the popover before the timer of the Tab that reached
		// its button runs; that timer must leave the new popover open.
		step(c.name + ": opening before a Tab's timer")
		if !page.MustEval(`async id => {
			const p = document.getElementById(id), button = (`+invoker+`)(id);
			if (p.matches(':popover-open')) p.hidePopover();
			button.focus();
			document.dispatchEvent(new KeyboardEvent('keydown', {key: 'Tab', bubbles: true}));
			button.click();
			await new Promise(r => setTimeout(r));
			return p.matches(':popover-open');
		}`, c.popover).Bool() {
			t.Fatalf("%s: a Tab's timer closed the popover Enter opened after it (at %s)", current, since())
		}
		// The same for a popover open at the Tab, closed and reopened before
		// its timer, with focus outside it.
		if !page.MustEval(`async id => {
			const p = document.getElementById(id), button = (`+invoker+`)(id);
			if (!p.matches(':popover-open')) p.showPopover();
			button.focus();
			document.dispatchEvent(new KeyboardEvent('keydown', {key: 'Tab', bubbles: true}));
			p.hidePopover();
			p.showPopover();
			button.focus();
			await new Promise(r => setTimeout(r));
			return p.matches(':popover-open');
		}`, c.popover).Bool() {
			t.Fatalf("%s: a Tab's timer closed the popover reopened after it (at %s)", current, since())
		}

		// Opening the account menu from the keyboard moves focus into it,
		// since the phone sheet covers its own button (#1102). The create
		// popover is left out: focus stays on its button, which it overlaps
		// at the lower edge.
		if c.popover == "account-menu" {
			for _, key := range []struct {
				name string
				key  input.Key
			}{{"Enter", input.Enter}, {"Space", input.Space}} {
				step(c.name + ": opening with " + key.name)
				page.MustEval(closeAndFocus, c.popover)
				device(key.name+" on the button", func(p *rod.Page) { p.Keyboard.MustType(key.key) })
				check("opening moves focus into the popover", inside, c.popover)
			}
		}

		step(c.name + ": Tab past the last control")
		open()
		for i := 2; page.MustEval(isOpen, c.popover).Bool(); i++ {
			if i > 10 {
				t.Fatalf("%s: the popover is still open after %d Tabs", current, i-1)
			}
			tabKey(fmt.Sprintf("Tab %d", i), false)
			check(fmt.Sprintf("after Tab %d focus is inside the open popover or it is closed", i), settled, c.popover)
		}
		if page.MustEval(onInvoker, c.popover).Bool() {
			t.Fatalf("%s: closing moved focus back to the button", current)
		}

		step(c.name + ": Shift-Tab before the first control")
		open()
		tabKey("Shift-Tab", true)
		check("after Shift-Tab focus is inside the open popover or it is closed", settled, c.popover)

		step(c.name + ": Escape")
		open()
		device("Escape", func(p *rod.Page) { p.Keyboard.MustType(input.Escape) })
		check("Escape closes the popover with focus on its button", `id => !(`+isOpen+`)(id) && (`+onInvoker+`)(id)`, c.popover)

		step(c.name + ": an outside click")
		open()
		device("clicking "+c.outside, func(p *rod.Page) { p.MustElement(c.outside).MustClick() })
		check("the click closes the popover with focus on "+c.landed, `(id, landed) => !(`+isOpen+`)(id) && document.activeElement === document.querySelector(landed)`, c.popover, c.landed)
	}
}
