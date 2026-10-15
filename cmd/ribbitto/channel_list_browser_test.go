package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestChannelListBrowser drives the channel list page at phone and desktop
// widths through the production handler, stream and scripts (#969).
func TestChannelListBrowser(t *testing.T) {
	unavailable := func(reason string) {
		t.Helper()
		if os.Getenv("RIBBITTO_REQUIRE_BROWSER") == "1" {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	bin, found := launcher.LookPath()
	if !found {
		unavailable("no installed browser; Claude runs the channel list locally")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	control, err := launch.Launch()
	if err != nil {
		unavailable(err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(control).MustConnect().MustIgnoreCertErrors(true)
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })

	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "list", "general")
	// Enough channels that the phone list scrolls, with the expanded one far down.
	var expanded conversationtest.ChannelFixture
	for i := 1; i <= 30; i++ {
		c := conversationtest.Channel(t, pool, f.OrganizationID, fmt.Sprintf("room%02d", i), false)
		if i == 25 {
			expanded = c
		}
	}
	conversationtest.Topic(t, pool, f.OrganizationID, expanded.ID, "plans")
	account := identitytest.Account(t, pool, "reader@example.org", "reader")
	orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	handler, _, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
	acceptanceOK(t, err)
	var mu sync.Mutex
	var streams, pages []string
	reads := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/events") {
			streams = append(streams, r.URL.RequestURI())
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/read") {
			reads++
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/events") {
			handler.ServeHTTP(w, r)
			return
		}
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		handler.ServeHTTP(recorder, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			mu.Lock()
			pages = append(pages, fmt.Sprintf("%s %s %d", r.Method, r.URL.Path, recorder.status))
			mu.Unlock()
		}
	}))
	defer server.Close()
	recorded := func() ([]string, int) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), streams...), reads
	}
	owner, reader := sidebarBrowser(t, pool, server, f.AccountID), sidebarBrowser(t, pool, server, account)

	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	for _, cookie := range reader.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: server.URL, Path: "/", Secure: true})
	}
	page.MustSetViewport(390, 844, 1, false)
	// Count every EventSource and sidebar frame without replacing the transport.
	page.MustEvalOnNewDocument(`window.sources = []; window.recounts = 0;
		const Native = window.EventSource;
		window.EventSource = class extends Native { constructor(...args) { super(...args); window.sources.push(this); } };
		document.addEventListener('htmx:sseMessage', e => { if (e.detail.type === 'sidebar') window.recounts++; });`)
	general, row := view.ChannelDOMID(f.Channel.ID), view.ChannelDOMID(expanded.ID)
	idText := func(id kernel.ID) string {
		return strings.TrimPrefix(view.ChannelURL("list", id), "/organizations/list/channels/")
	}
	// Scoped to the row's own link: the expanded row also holds its topic rows.
	// Tap targets below desk: the add button 44 × 44, every row link 44 tall and as
	// wide as its row.
	const state = `(general, row) => {
		const main = document.querySelector('main'), stream = document.getElementById('organization-stream');
		const open = window.sources.filter(s => s.readyState !== EventSource.CLOSED).length;
		const badge = document.getElementById('link-' + general).querySelector('[data-unread-count]:not([hidden]) > .sr-only')?.previousElementSibling?.textContent || '';
		const box = e => e.getBoundingClientRect(), add = box(document.querySelector('[popovertarget=channel-create]'));
		const small = [...document.querySelectorAll('#sidebar-channels a')].filter(a => box(a).height < 44 || box(a).width < box(a.parentElement).width - 1).map(a => a.id);
		if (add.width < 44 || add.height < 44) small.push('add');
		return { url: stream.getAttribute('sse-connect'), cursor: stream.dataset.eventCursor, open, badge,
			recounts: window.recounts, scroll: main.scrollTop, mainWidth: box(main).width, small,
			mains: document.querySelectorAll('main').length, mainShown: main.checkVisibility() && main.contains(document.getElementById('sidebar-channels')),
			promptShown: main.nextElementSibling.checkVisibility(), nested: !!document.querySelector('#' + row + ' > #sidebar-topics'),
			reads: !!document.getElementById('read-form') };
	}`
	type listState struct {
		URL, Cursor, Badge string
		Open, Recounts     int
		Scroll, MainWidth  float64
		Small              []string
		Mains              int
		MainShown, Nested  bool
		PromptShown, Reads bool
	}
	read := func() listState {
		var s listState
		acceptanceOK(t, page.MustEval(state, general, row).Unmarshal(&s))
		return s
	}
	wantURL := func(cursor string) string {
		return "/organizations/list/events?after=" + cursor + "&want=sidebar&channel=" + idText(expanded.ID)
	}

	page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
	page.MustWait(`() => window.recounts >= 1 && !document.querySelector('.htmx-settling')`)
	first := read()
	if first.URL != wantURL(first.Cursor) || first.Open != 1 || first.MainWidth < 280 || first.Mains != 1 || !first.MainShown || first.PromptShown ||
		!first.Nested || first.Reads || first.Scroll == 0 || len(first.Small) != 0 {
		t.Fatalf("phone list page: %+v, want stream %s", first, wantURL(first.Cursor))
	}
	if got, _ := recorded(); len(got) != 1 || got[0] != first.URL {
		t.Fatalf("stream requests %v, want exactly %s", got, first.URL)
	}

	post(t, owner, view.ChannelURL("list", f.Channel.ID), "first")
	// Another member's post reaches the reader as an unread frame, not a recount.
	page.MustWait(`general => document.getElementById('link-' + general).querySelector('[data-unread-count]:not([hidden]) > .sr-only')?.previousElementSibling?.textContent === '1'`, general)
	live := read()
	if live.Badge != "1" || live.Recounts != first.Recounts || live.Scroll != first.Scroll || !live.Nested || live.Open != 1 || live.URL != first.URL || len(live.Small) != 0 {
		t.Fatalf("live count: %+v, want count 1 without a recount at scroll %v, still nested", live, first.Scroll)
	}

	// A reconnect recounts from the database, so a corrupted count is restored.
	reconnect := `() => { const source = window.sources.at(-1); source.close(); source.dispatchEvent(new Event('error')); }`
	page.MustEval(`id => { document.getElementById('link-' + id).querySelector('.sr-only').previousElementSibling.textContent = '999'; }`, general)
	page.MustEval(reconnect)
	page.MustWait(`(n) => window.recounts > n && !document.querySelector('.htmx-settling')`, live.Recounts)
	again := read()
	if got, _ := recorded(); again.Badge != "1" || again.Open != 1 || len(got) != 2 || got[1] != first.URL {
		t.Fatalf("reconnect: %+v, stream requests %v", again, got)
	}

	// An expired cursor makes the next connection reset; the page reloads with
	// a fresh snapshot and cursor and keeps counting on one stream.
	_, err = pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = event_seq WHERE id = $1", f.OrganizationID)
	acceptanceOK(t, err)
	page.MustEval(`() => { window.beforeReset = true; }`)
	page.MustEval(reconnect)
	page.MustWait(`() => !window.beforeReset && window.recounts >= 1 && !document.querySelector('.htmx-settling')`)
	reset := read()
	got, _ := recorded()
	if reset.Cursor == first.Cursor || reset.URL != wantURL(reset.Cursor) || reset.Badge != "1" || reset.Open != 1 || !reset.Nested ||
		len(got) != 4 || got[2] != first.URL || got[3] != reset.URL {
		t.Fatalf("reset recovery: %+v, stream requests %v", reset, got)
	}
	post(t, owner, view.ChannelURL("list", f.Channel.ID), "second")
	page.MustWait(`general => document.getElementById('link-' + general).querySelector('.sr-only')?.previousElementSibling?.textContent === '2' && !document.querySelector('.htmx-settling')`, general)
	if s := read(); s.Open != 1 {
		t.Fatalf("resumed counts on %d streams", s.Open)
	}
	if _, n := recorded(); n != 0 {
		t.Fatalf("the list page sent %d reading POSTs", n)
	}

	// The creation popover stays inside narrow screens, with 44 px controls.
	for _, size := range [][2]int{{320, 568}, {390, 844}} {
		page.MustSetViewport(size[0], size[1], 1, false)
		page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
		page.MustElement("[popovertarget=channel-create]").MustClick()
		page.MustWait(`() => document.getElementById('channel-create').matches(':popover-open')`)
		if problem := page.MustEval(`() => {
			const form = document.getElementById('channel-create'), box = form.getBoundingClientRect();
			if (box.left < 0 || box.top < 0 || box.right > innerWidth || box.bottom > innerHeight) return 'outside the viewport: ' + JSON.stringify(box);
			if (document.documentElement.scrollWidth > innerWidth) return 'the page scrolls sideways';
			const small = [...form.querySelectorAll('input:not([type=radio]), button, label:has(input[type=radio])')].filter(e => e.getBoundingClientRect().height < 44);
			return small.length ? 'controls under 44 px: ' + small.length : '';
		}`).Str(); problem != "" {
			t.Fatalf("%d px popover: %s", size[0], problem)
		}
	}

	// The bottom navigation (#970) ends the list's pane above the safe area
	// (a 34 px home indicator); Home is this page, and Account opens the
	// shared account menu as a bottom sheet.
	_, err = page.Call(ctx, string(page.SessionID), "Emulation.setSafeAreaInsetsOverride", map[string]any{"insets": map[string]int{"bottom": 34}})
	acceptanceOK(t, err)
	const homeLink, bottomAccount = `a[aria-current=page][href="/organizations/list/channels"]`, "#organization-stream > nav:last-of-type [popovertarget=account-menu]"
	page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
	if problem := page.MustEval(`(homeLink, bottomAccount) => {
		const home = document.querySelector(homeLink), account = document.querySelector(bottomAccount), bar = home?.closest('nav');
		if (!bar?.checkVisibility() || bar !== account?.closest('nav')) return 'no visible bar with Home and Account';
		if (account.hasAttribute('aria-current')) return 'Account has aria-current';
		const box = e => e.getBoundingClientRect(), main = document.querySelector('main');
		if ([home, account].some(e => box(e).width < 44 || box(e).height < 44)) return 'an item under 44 × 44';
		if (getComputedStyle(bar).paddingBottom !== '34px' || box(bar).bottom !== innerHeight) return 'not above the safe area: ' + getComputedStyle(bar).paddingBottom;
		main.scrollTop = main.scrollHeight;
		const last = box(document.querySelector('#sidebar-channels > li:last-child'));
		return last.bottom > box(bar).top || last.bottom > box(main).bottom ? 'the bar covers the last row' : '';
	}`, homeLink, bottomAccount).Str(); problem != "" {
		t.Fatalf("phone bottom navigation: %s", problem)
	}
	sheet := func(open bool) {
		t.Helper()
		page.MustWait(`open => document.getElementById('account-menu').matches(':popover-open') === open`, open)
	}
	// accountKeys drives the menu from its visible invoker, with or without
	// scripts: Enter and Space each open it, and repeating the key keeps it
	// open, since opening moves focus to its first line (#1102); Escape closes
	// it from inside with focus back on the invoker, and an outside tap closes it.
	// After each step the invoker's accessibility node reports the state.
	accountKeys := func(p *rod.Page, invoker string, tap func()) {
		t.Helper()
		step := func(name string, open bool, keys ...input.Key) {
			t.Helper()
			p.Keyboard.MustType(keys...)
			if err := p.Timeout(5 * time.Second).Wait(rod.Eval(`open => document.getElementById('account-menu').matches(':popover-open') === open`, open)); err != nil {
				t.Fatalf("%s %s: the menu is not open=%t: %v", invoker, name, open, err)
			}
			tree, err := proto.AccessibilityGetPartialAXTree{ObjectID: p.MustElement(invoker).Object.ObjectID}.Call(p)
			acceptanceOK(t, err)
			got := "absent"
			for _, property := range tree.Nodes[0].Properties {
				if property.Name == proto.AccessibilityAXPropertyNameExpanded {
					got = fmt.Sprint(property.Value.Value.Bool())
				}
			}
			if got != fmt.Sprint(open) {
				t.Fatalf("%s %s: expanded %s, want %t", invoker, name, got, open)
			}
		}
		p.MustEval(`s => document.querySelector(s).focus()`, invoker)
		step("closed", false)
		step("Enter", true, input.Enter)
		step("Enter again", true, input.Enter)
		step("Escape", false, input.Escape)
		step("Space", true, input.Space)
		step("Space again", true, input.Space)
		step("Escape", false, input.Escape)
		step("Enter", true, input.Enter)
		p.Keyboard.MustType(input.Tab)
		if !p.MustEval(`() => !!document.activeElement.closest('#account-menu')`).Bool() {
			t.Fatalf("%s: Tab does not enter the menu", invoker)
		}
		step("Escape", false, input.Escape)
		if !p.MustEval(`s => document.activeElement === document.querySelector(s)`, invoker).Bool() {
			t.Fatalf("%s: Escape did not return focus", invoker)
		}
		step("Space", true, input.Space)
		tap()
		step("outside tap", false)
	}
	tapHeading := func() { page.MustElement("main h1").MustClick() }
	accountKeys(page, bottomAccount, tapHeading)
	page.MustElement(bottomAccount).MustClick()
	sheet(true)
	if problem := page.MustEval(`() => {
		const menu = document.getElementById('account-menu'), box = menu.getBoundingClientRect();
		if (box.left !== 0 || box.right !== innerWidth || box.bottom !== innerHeight) return 'not a full-width bottom sheet: ' + JSON.stringify(box);
		if (getComputedStyle(menu).paddingBottom !== '50px') return 'no safe-area padding: ' + getComputedStyle(menu).paddingBottom;
		return menu.querySelector('form[action="/signout"] button').getBoundingClientRect().height < 44 ? 'sign-out under 44 px' : '';
	}`).Str(); problem != "" {
		t.Fatalf("account sheet: %s", problem)
	}
	page.Keyboard.MustType(input.Escape)
	sheet(false)

	// A phone conversation hides the frame and leads back to its row.
	type frameState struct {
		Back, Rail, Aside, Bar bool
		Width, Height          float64
		Label                  string
	}
	frame := func() frameState {
		var s frameState
		acceptanceOK(t, page.MustEval(`homeLink => {
			const back = document.querySelector('header a[aria-label]'), box = back.getBoundingClientRect();
			return { back: back.checkVisibility(), width: box.width, height: box.height, label: back.getAttribute('aria-label'),
				rail: document.querySelector('nav').checkVisibility(), aside: document.querySelector('aside').checkVisibility(),
				bar: !!document.querySelector(homeLink)?.checkVisibility() };
		}`, homeLink).Unmarshal(&s))
		return s
	}
	page.MustNavigate(server.URL + view.ChannelURL("list", expanded.ID)).MustWaitLoad()
	if phone := frame(); !phone.Back || phone.Width < 44 || phone.Height < 44 || phone.Label != "Back to lilypads" || phone.Rail || phone.Aside || phone.Bar {
		t.Fatalf("phone conversation frame: %+v", phone)
	}
	page.MustElement("header a[aria-label]").MustClick()
	page.MustWait(`want => location.pathname + location.search + location.hash === want && document.readyState === 'complete'`,
		view.ChannelListURL("list", expanded.ID)+"#"+row)
	if !page.MustEval(`row => {
		const port = document.querySelector('main').getBoundingClientRect(), box = document.getElementById(row).getBoundingClientRect();
		return box.top >= port.top && box.top < port.bottom && !!document.querySelector('#' + row + ' > #sidebar-topics');
	}`, row).Bool() {
		t.Fatal("the back link did not open the list with the channel expanded and in view")
	}

	// viewport-fit=cover extends pages under a landscape notch (emulated 44 px
	// side insets); the body keeps content beside it, here and on sign-in
	// below, and at desktop width it adds nothing.
	insets := func(sides map[string]int) {
		_, err := page.Call(ctx, string(page.SessionID), "Emulation.setSafeAreaInsetsOverride", map[string]any{"insets": sides})
		acceptanceOK(t, err)
	}
	inset := func(want float64) string {
		return page.MustEval(`want => {
			// clientWidth leaves out a classic scrollbar (Linux CI), which a page
			// taller than the landscape viewport shows and innerWidth includes.
			const style = getComputedStyle(document.body), box = document.body.firstElementChild.getBoundingClientRect(), width = document.documentElement.clientWidth;
			if (!document.querySelector('meta[name=viewport]').content.includes('viewport-fit=cover')) return 'no viewport-fit=cover';
			if (style.paddingLeft !== want + 'px' || style.paddingRight !== want + 'px') return 'body padding ' + style.paddingLeft + ' ' + style.paddingRight;
			if (box.left !== want || box.right !== width - want) return 'content from ' + box.left + ' to ' + box.right + ', want ' + want + ' to ' + (width - want);
			return document.documentElement.scrollWidth > width ? 'the page scrolls sideways' : '';
		}`, want).Str()
	}
	insets(map[string]int{"left": 44, "right": 44, "bottom": 21})
	page.MustSetViewport(844, 390, 1, false)
	for _, path := range []string{view.ChannelListURL("list", expanded.ID), view.ChannelURL("list", expanded.ID)} {
		page.MustNavigate(server.URL + path).MustWaitLoad()
		if problem := inset(44); problem != "" {
			t.Fatalf("landscape %s: %s", path, problem)
		}
	}
	insets(map[string]int{"top": 0, "left": 0, "right": 0, "bottom": 0})

	// At desktop width the list is still the one main, beside the prompt, and a
	// conversation keeps its frame and hides the link.
	page.MustSetViewport(1280, 800, 1, false)
	page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
	page.MustWait(`() => window.recounts >= 1`)
	if desktop := read(); desktop.Mains != 1 || !desktop.MainShown || !desktop.PromptShown {
		t.Fatalf("desktop list page: %+v", desktop)
	}
	if problem := inset(0); problem != "" {
		t.Fatalf("desktop list page: %s", problem)
	}
	page.MustNavigate(server.URL + view.ChannelURL("list", expanded.ID)).MustWaitLoad()
	if desktop := frame(); desktop.Back || !desktop.Rail || !desktop.Aside || desktop.Bar {
		t.Fatalf("desktop conversation frame: %+v", desktop)
	}
	// The desktop list page hides the bottom navigation; the app bar's button
	// opens the same menu where it always opened.
	page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
	const barAccount = "#organization-stream > nav:first-of-type [popovertarget=account-menu]"
	accountKeys(page, barAccount, tapHeading)
	page.MustElement(barAccount).MustClick()
	sheet(true)
	if problem := page.MustEval(`homeLink => {
		const box = document.getElementById('account-menu').getBoundingClientRect();
		if (document.querySelector(homeLink)?.checkVisibility()) return 'the bottom navigation shows';
		return box.left !== 96 || box.width !== 256 || box.bottom !== innerHeight - 12 ? 'menu moved: ' + JSON.stringify(box) : '';
	}`, homeLink).Str(); problem != "" {
		t.Fatalf("desktop account menu: %s", problem)
	}

	// Without JavaScript, at phone width: the back link's fragment alone brings
	// the row into view, and a failed creation can be corrected in place.
	plain := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer plain.MustClose()
	acceptanceOK(t, proto.EmulationSetScriptExecutionDisabled{Value: true}.Call(plain))
	plain.MustSetViewport(390, 844, 1, false)
	// rod's element clicks and input did not return with scripts disabled, so
	// press the mouse at the element's centre and insert text into the focused
	// field; landed polls for the expected document.
	landed := func(want string) {
		plain.MustWait(`want => location.pathname + location.search + location.hash === want && document.readyState === 'complete'`, want)
	}
	click := func(selector string) {
		var at struct{ X, Y float64 }
		acceptanceOK(t, plain.MustEval(`s => { const e = document.querySelector(s); e.scrollIntoView({block: 'center'});
			const r = e.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; }`, selector).Unmarshal(&at))
		plain.Mouse.MustMoveTo(at.X, at.Y).MustClick(proto.InputMouseButtonLeft)
	}
	const inView = `row => {
		const port = document.querySelector('main').getBoundingClientRect(), box = document.getElementById(row).getBoundingClientRect();
		return box.top >= port.top && box.top < port.bottom;
	}`
	plain.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID))
	landed(view.ChannelListURL("list", expanded.ID))
	if plain.MustEval(inView, row).Bool() {
		t.Fatal("the expanded row starts in view, so the fragment proves nothing")
	}
	accountKeys(plain, bottomAccount, func() { click("main h1") })
	plain.MustNavigate(server.URL + view.ChannelURL("list", expanded.ID))
	landed(view.ChannelURL("list", expanded.ID))
	click("header a[aria-label]")
	landed(view.ChannelListURL("list", expanded.ID) + "#" + row)
	if !plain.MustEval(inView, row).Bool() {
		t.Fatal("without JavaScript the back link did not bring the row into view")
	}
	click("[popovertarget=channel-create]")
	click("#channel-name")
	plain.MustInsertText("general")
	click("#channel-create button[type=submit]")
	landed("/organizations/list/channels")
	if !plain.MustEval(`() => {
		const form = document.getElementById('channel-create'), field = document.getElementById('channel-name');
		const error = document.getElementById(field.getAttribute('aria-describedby'));
		return field.getAttribute('aria-invalid') === 'true' && !form.hasAttribute('popover') && form.checkVisibility() && error?.getAttribute('role') === 'alert' && error.checkVisibility() && error.textContent.trim() !== '';
	}`).Bool() {
		t.Fatal("the failed form is not in place with a visible error linked to its field")
	}
	click("#channel-name")
	if !plain.MustEval(`() => document.activeElement.id === 'channel-name' && (document.activeElement.select(), true)`).Bool() {
		t.Fatal("the invalid field does not take focus")
	}
	plain.MustInsertText("planning")
	click("#channel-create button[type=submit]")
	plain.MustWait(`() => document.readyState === 'complete' && /^\/organizations\/list\/channels\/[0-9a-f-]{36}$/.test(location.pathname) && document.title.startsWith('planning ·')`)
	mu.Lock()
	tail := append([]string(nil), pages[len(pages)-4:]...)
	mu.Unlock()
	created := plain.MustEval(`() => location.pathname`).Str()
	if want := []string{"POST /organizations/list/channels 422", "POST /organizations/list/channels 303", "GET " + created + " 200"}; !slices.Equal(tail[1:], want) {
		t.Fatalf("creation requests %v, want %v", tail, want)
	}

	// Last, since it ends the session both pages share: signing out from the
	// phone sheet lands on sign-in, and the old session no longer opens the list.
	listURL := view.ChannelListURL("list", expanded.ID)
	reader.visit(t, "GET", listURL, nil, 200)
	// Front the page again: a background tab paints no frames to click on.
	page.MustActivate().MustSetViewport(390, 844, 1, false)
	page.MustNavigate(server.URL + listURL).MustWaitLoad()
	page.MustElement(bottomAccount).MustClick()
	sheet(true)
	page.MustElement("#account-menu form[action='/signout'] button").MustClick()
	page.MustWait(`() => location.pathname === '/signin' && document.readyState === 'complete'`)
	reader.visit(t, "GET", listURL, nil, 404)
	insets(map[string]int{"left": 44, "right": 44})
	page.MustSetViewport(844, 390, 1, false)
	if problem := inset(44); problem != "" {
		t.Fatalf("landscape sign-in: %s", problem)
	}
}

// statusRecorder keeps a page response's status; Unwrap keeps the
// ResponseController behind it working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
