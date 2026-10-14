package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
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
	var streams []string
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
		handler.ServeHTTP(w, r)
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
		const badge = document.getElementById('link-' + general).querySelector('.sr-only')?.previousElementSibling?.textContent || '';
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
	page.MustWait(`(general, n) => window.recounts > n && document.getElementById('link-' + general).querySelector('.sr-only') && !document.querySelector('.htmx-settling')`, general, first.Recounts)
	live := read()
	if live.Badge != "1" || live.Scroll != first.Scroll || !live.Nested || live.Open != 1 || live.URL != first.URL || len(live.Small) != 0 {
		t.Fatalf("live recount: %+v, want count 1 at scroll %v, still nested", live, first.Scroll)
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

	// At desktop width the list is still the one main, beside the prompt.
	page.MustSetViewport(1280, 800, 1, false)
	page.MustNavigate(server.URL + view.ChannelListURL("list", expanded.ID)).MustWaitLoad()
	page.MustWait(`() => window.recounts >= 1`)
	if desktop := read(); desktop.Mains != 1 || !desktop.MainShown || !desktop.PromptShown {
		t.Fatalf("desktop list page: %+v", desktop)
	}
}
