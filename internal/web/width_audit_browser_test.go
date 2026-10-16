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
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testWidthAuditBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const css = "/static/css/app.css"
	longName := strings.Repeat("LongName", 8)
	cursor := int64(42)
	messages := streamTestMessages(kernel.ID{2}, 7, 8, 9)
	for i := range messages {
		messages[i].DisplayName, messages[i].Handle = longName, "long_member_handle"
		messages[i].TopicName, messages[i].TopicURL = longName, view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2})
		messages[i].CreatedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		messages[i].Body = "https://example.org/" + strings.Repeat("longpath", 60) + "\nconst value = " + strings.Repeat("code_without_spaces", 30) + "\n" + strings.Repeat("日本語の長い文章", 60)
	}
	// A one-character topic name keeps the message's topic link narrow.
	messages[len(messages)-1].TopicName = "短"
	base := view.ChannelPage{Organization: view.Organization{Name: longName, Slug: "acme"},
		Current:     view.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: longName, ImageURL: view.ChannelImageURL("lake")},
		DisplayName: longName, Handle: "long_member_handle", Role: "owner", Messages: messages,
		Topics:    []view.Topic{{ID: kernel.ID{2}, Name: longName}, {ID: kernel.ID{3}, Name: "短"}},
		ImageKeys: []string{"lake", "flowers", "moon", "kite"}, Reading: true, FirstUnread: 7, Older: true, EventCursor: &cursor}
	// Unbroken author and pinner names must wrap inside the overview (#889).
	base.Pins = []view.Pin{{MessageID: kernel.ID{7}, Excerpt: conversation.PinExcerpt(messages[0].Body), AuthorDisplayName: longName, AuthorHandle: "long_member_handle", Topic: view.Topic{Name: longName}, PinnerDisplayName: longName, PinnerHandle: "long_member_handle", PinnedAt: messages[0].CreatedAt}}
	for i := range 20 {
		base.Channels = append(base.Channels, view.Channel{ID: kernel.ID{byte(i + 1)}, Name: longName, ImageURL: base.Current.ImageURL})
	}
	members := &view.MemberPage{Next: &kernel.ID{30}}
	for i := range 20 {
		members.Items = append(members.Items, view.Member{ID: kernel.ID{byte(i + 1)}, DisplayName: longName, Handle: "long_member_handle"})
	}
	cases := []struct {
		name string
		page templ.Component
	}{
		{"setup", view.Setup(css, view.SetupForm{})},
		{"signin", view.SignIn(css, view.SignInForm{SignUpOpen: true})},
		{"signup", view.SignUp(css, view.SignUpForm{})},
		{"home", view.Hello(css, view.Viewer{SignUpOpen: true})},
		{"home-signed-in", view.Hello(css, view.Viewer{SignedIn: true, DisplayName: longName})},
		{"rate-limit", view.TooManyRequests(css)},
	}
	for _, topic := range []bool{false, true} {
		for _, panel := range []string{"closed", view.PanelMembers, view.PanelOverview} {
			p := base
			name := "feed"
			if topic {
				name, p.Topic = "topic", &p.Topics[0]
			}
			if panel != "closed" {
				p.Panel, p.Members = panel, members
			}
			cases = append(cases, struct {
				name string
				page templ.Component
			}{name + "-" + panel, view.ChannelScreen(css, p)})
		}
	}
	base.List = true
	cases = append(cases, struct {
		name string
		page templ.Component
	}{"channel-list", view.ChannelListScreen(css, base)})
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	// Keep the ordinary stream and reading scripts running against idle fakes;
	// the layout fixture must include the cursor-dependent Mark as read targets.
	mux.HandleFunc("GET /organizations/acme/events", idleStream)
	mux.HandleFunc("POST /organizations/acme/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, c := range cases {
		path := "/" + c.name
		if c.name == "home" {
			path = "/{$}"
		}
		mux.Handle(path, catalogues.Middleware(templ.Handler(c.page)))
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "width audit")
	defer closeTab()
	setup := pageStep(ctx, tab)
	defer deleteLangCookie(t, tab, server.URL)
	for _, size := range [][2]int{{320, 568}, {390, 844}, {851, 844}, {852, 844}, {1199, 844}, {1200, 844}, {844, 390}, {320, 200}, {640, 400}, {1280, 400}, {1280, 200}} {
		setup(t, fmt.Sprintf("setting the viewport to %dx%d", size[0], size[1]), func(p *rod.Page) { p.MustSetViewport(size[0], size[1], 1, false) })
		for _, lang := range []string{"en", "ja"} {
			setup(t, "setting the lang cookie to "+lang, func(p *rod.Page) {
				p.MustSetCookies(&proto.NetworkCookieParam{Name: "lang", Value: lang, URL: server.URL})
			})
			for _, c := range cases {
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size[0], size[1], lang, c.name), func(t *testing.T) {
					where := fmt.Sprintf("page=%s viewport=%dx%d language=%s", c.name, size[0], size[1], lang)
					run := pageStep(ctx, tab)
					step := func(what string, fn func(p *rod.Page)) {
						t.Helper()
						run(t, where+": "+what, fn)
					}
					audit := func(p *rod.Page, state string) {
						result := p.MustEval(widthAudit)
						// The rate-limit page is the one page without a control.
						if result.Get("checked").Int() == 0 && c.name != "rate-limit" {
							t.Errorf("%s state=%s: no visible control audited", where, state)
						}
						for _, problem := range result.Get("problems").Arr() {
							t.Errorf("%s state=%s: %s", where, state, problem.Str())
						}
						// Below the short height (#1056) a conversation scrolls as a
						// whole around its message list, which must stay the scrolling
						// pane the history, composer and stream scripts move.
						if size[1] < shortHeight && strings.HasSuffix(c.name, "closed") && (state == "loaded" || state == "move-form-visible") {
							for _, problem := range p.MustEval(shortConversationAudit, state != "loaded").Arr() {
								t.Errorf("%s state=%s: %s", where, state, problem.Str())
							}
						}
					}
					step("loading", func(p *rod.Page) {
						path := "/" + c.name
						if c.name == "home" {
							path = "/"
						}
						p.MustNavigate(server.URL + path).MustWaitLoad()
						// Wait for layout and panel modality before scanning active targets.
						p.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
					})
					step("auditing the loaded page", func(p *rod.Page) { audit(p, "loaded") })
					// The phone header has no gap between its items to fit at 320 px,
					// but the channel name keeps one from the image (#1095).
					if size == [2]int{390, 844} && strings.HasSuffix(c.name, "-closed") {
						step("measuring the gap after the channel image", func(p *rod.Page) {
							if problem := p.MustEval(headerImageGap).Str(); problem != "" {
								t.Errorf("%s: %s", where, problem)
							}
						})
					}
					if strings.HasSuffix(c.name, "overview") {
						step("opening the image picker", func(p *rod.Page) {
							p.MustElement("#side-panel summary").MustClick()
							p.MustWait(`() => document.querySelector('#side-panel details').open`)
							audit(p, "image-picker-open")
						})
					}
					if strings.HasSuffix(c.name, "closed") {
						step("checking a message", func(p *rod.Page) {
							p.MustElement(`#message-items input[type=checkbox]`).MustClick()
							p.MustWait(`() => document.querySelector('#message-items input[type=checkbox]:checked') &&
								document.getElementById('branch-form').checkVisibility()`)
							audit(p, "move-form-visible")
						})
					}
					if c.name == "channel-list" || strings.HasSuffix(c.name, "closed") {
						for _, id := range []string{"channel-create", "account-menu"} {
							step("opening "+id, func(p *rod.Page) {
								if !p.MustEval(`id => [...document.querySelectorAll('[popovertarget="' + id + '"]')].some(e => e.checkVisibility())`, id).Bool() {
									// Phone conversations expose these controls on the list page;
									// anywhere else a missing invoker would skip the audit silently.
									if c.name == "channel-list" || size[0] >= 852 {
										t.Errorf("%s: no visible invoker for %s", where, id)
									}
									return
								}
								p.MustEval(`id => [...document.querySelectorAll('[popovertarget="' + id + '"]')].find(e => e.checkVisibility()).click()`, id)
								p.MustWait(`id => document.getElementById(id).matches(':popover-open')`, id)
								audit(p, id+"-open")
								p.MustEval(`id => document.getElementById(id).hidePopover()`, id)
							})
						}
					}
				})
			}
		}
	}
}

// Check reachability after scrolling only user-scrollable ancestors. Calling
// scrollIntoView would also scroll overflow:hidden, concealing clipped controls.
// A top-layer popover is independent of its DOM ancestors' clipping and scroll.
const widthAudit = `() => {
	const root = document.documentElement, problems = [], box = e => e.getBoundingClientRect();
	const name = e => e.getAttribute('aria-label') || [...(e.labels || [])].map(l => l.textContent.trim()).join(' ') ||
		(e.getAttribute('aria-labelledby') || '').split(' ').map(id => document.getElementById(id)?.textContent || '').join(' ') || e.textContent.trim();
	const describe = e => e.tagName.toLowerCase() + (e.id ? '#' + e.id : '.' + [...e.classList].join('.')) +
		' name=' + JSON.stringify(name(e).slice(0, 100)) + ' box=' + JSON.stringify(box(e).toJSON());
	const shown = e => e.checkVisibility({visibilityProperty:true});
	const scrolls = value => value === 'auto' || value === 'scroll';
	if (root.scrollWidth > root.clientWidth) problems.push('horizontal overflow: ' + describe(root) + ' scrollWidth=' + root.scrollWidth + ' clientWidth=' + root.clientWidth);
	for (const e of document.querySelectorAll('*')) {
		if (scrolls(getComputedStyle(e).overflowX) && e.scrollWidth > e.clientWidth)
			problems.push('horizontal scroll container: ' + describe(e) + ' scrollWidth=' + e.scrollWidth + ' clientWidth=' + e.clientWidth);
	}
	const controls = [...document.querySelectorAll('a[href],button,input:not([type=hidden]),select,textarea,summary,label')];
	const positions = [...document.querySelectorAll('*')].filter(e => scrolls(getComputedStyle(e).overflowX) || scrolls(getComputedStyle(e).overflowY)).map(e => [e,e.scrollLeft,e.scrollTop]);
	const windowPosition = [window.scrollX, window.scrollY];
	const toggle = 'input[type=checkbox],input[type=radio]';
	let checked = 0;
	for (const e of controls) {
		if (!shown(e) || e.closest('[inert]')) continue;
		// A checkbox or radio label is the target that stands in for the tiny
		// native widget, so it is audited instead of the widget. Any other
		// control's caption is an equivalent target (docs/ui.md): exempt only
		// while the control itself is shown and at least 44 x 44.
		if (e.tagName === 'LABEL' && e.control && !e.control.matches(toggle) && shown(e.control) &&
			box(e.control).width >= 44 && box(e.control).height >= 44) continue;
		if (e.matches(toggle) && [...e.labels].some(shown)) continue;
		checked++;
		// Only inline links in running text are exempt: an inline <a> whose
		// paragraph also has its own text. A paragraph used as a flex wrapper
		// for a paging control, or a link styled as a block or button, is not
		// running text and still needs a phone target.
		const p = e.closest('p');
		const proseLink = e.matches('a[href]') && getComputedStyle(e).display === 'inline' && p &&
			[...p.childNodes].some(n => n.nodeType === Node.TEXT_NODE && n.textContent.trim());
		let r = box(e);
		if (matchMedia('(width < 852px)').matches && !proseLink && (r.width < 44 || r.height < 44))
			problems.push('phone target smaller than 44x44: ' + describe(e));
		const ancestors = [];
		for (let a = e.parentElement; a; a = a.parentElement) {
			ancestors.push(a);
			if (a.matches(':popover-open')) break;
		}
		for (const a of ancestors) {
			if (a === root || a === document.body) continue;
			const s = getComputedStyle(a), port = box(a);
			r = box(e);
			if (scrolls(s.overflowY)) a.scrollTop += r.top - port.top - a.clientTop;
			if (scrolls(s.overflowX)) a.scrollLeft += r.left - port.left - a.clientLeft;
		}
		if (!e.closest(':popover-open') && ![e,...ancestors].some(a => getComputedStyle(a).position === 'fixed') &&
			!['hidden','clip'].includes(getComputedStyle(root).overflowY) && !['hidden','clip'].includes(getComputedStyle(document.body).overflowY))
			window.scrollTo({top: window.scrollY + box(e).top, behavior:'instant'});
		r = box(e);
		if (r.left < -0.5 || r.right > root.clientWidth + 0.5 || r.top < -0.5 || r.bottom > root.clientHeight + 0.5)
			problems.push('control outside viewport ' + root.clientWidth + 'x' + root.clientHeight + ': ' + describe(e));
		for (const a of ancestors) {
			if (a === root || a === document.body) continue;
			const s = getComputedStyle(a), port = box(a), left = port.left + a.clientLeft, top = port.top + a.clientTop;
			if ((s.overflowX !== 'visible' && (r.left < left - 0.5 || r.right > left + a.clientWidth + 0.5)) ||
				(s.overflowY !== 'visible' && (r.top < top - 0.5 || r.bottom > top + a.clientHeight + 0.5)))
				problems.push('control clipped by ' + describe(a) + ': ' + describe(e));
		}
	}
	for (const [e,x,y] of positions) { e.scrollLeft = x; e.scrollTop = y; }
	window.scrollTo({left:windowPosition[0],top:windowPosition[1],behavior:'instant'});
	return {problems, checked};
}`

// headerImageGap checks that at least 4 px separate the conversation header's
// channel image from the channel name after it.
const headerImageGap = `() => {
	const image = document.querySelector('#conversation > header img'), name = image.nextElementSibling;
	const gap = name.getBoundingClientRect().left - image.getBoundingClientRect().right;
	return gap >= 4 ? '' : 'channel image ' + JSON.stringify(image.getBoundingClientRect().toJSON()) + ' is ' + gap +
		' px from the channel name <' + name.tagName.toLowerCase() + '> ' + JSON.stringify(name.getBoundingClientRect().toJSON()) + ', want at least 4 px';
}`

// shortHeight is the short variant's 27.25 rem in web/styles/app.css.
const shortHeight = 436

// shortConversationAudit checks a conversation below the short height: the
// list is still a pane scrolled to the newest message on load, at least four
// 22 px lines of the first and the last message can be scrolled into view
// inside it, and a shown move form follows the list in the column instead of
// covering it, in place of the hidden composer.
const shortConversationAudit = `moving => {
	const root = document.documentElement, list = document.getElementById('message-list'), problems = [], box = e => e.getBoundingClientRect();
	const scrolls = e => ['auto','scroll'].includes(getComputedStyle(e).overflowY);
	const positions = [...document.querySelectorAll('*')].filter(scrolls).map(e => [e,e.scrollTop]);
	const windowTop = window.scrollY;
	if (!scrolls(list) || list.scrollHeight <= list.clientHeight)
		problems.push('message list is not a scrolling pane: clientHeight=' + list.clientHeight + ' scrollHeight=' + list.scrollHeight);
	if (!moving && list.scrollHeight - list.scrollTop - list.clientHeight > 2)
		problems.push('message list not at the newest message on load: scrollTop=' + list.scrollTop + ' scrollHeight=' + list.scrollHeight + ' clientHeight=' + list.clientHeight);
	const items = [...document.querySelectorAll('#message-items > li[data-event-seq]:not(#unread-divider)')];
	for (const item of [items[0], items.at(-1)]) {
		list.scrollTop += box(item).top - box(list).top - list.clientTop;
		for (let a = list.parentElement; a && a !== document.body; a = a.parentElement)
			if (scrolls(a)) a.scrollTop += box(list).top - box(a).top - a.clientTop;
		window.scrollTo({top: window.scrollY + box(list).top, behavior:'instant'});
		const r = box(item), top = Math.max(r.top, box(list).top + list.clientTop, 0);
		const shown = Math.max(0, Math.min(r.bottom, box(list).top + list.clientTop + list.clientHeight, root.clientHeight) - top);
		if (shown < Math.min(r.height, 88) - 0.5)
			problems.push('message ' + item.id + ' shows ' + shown + ' px after scrolling (list clientHeight=' + list.clientHeight + ', viewport ' + root.clientWidth + 'x' + root.clientHeight + ')');
	}
	if (moving) {
		const form = document.getElementById('branch-form');
		if (box(form).top < box(list).bottom - 0.5)
			problems.push('move form overlaps the message list: form top=' + box(form).top + ' list bottom=' + box(list).bottom);
		if (document.getElementById('message-composer').checkVisibility())
			problems.push('composer still in the column beside the move form');
	}
	for (const [e,y] of positions) e.scrollTop = y;
	window.scrollTo({top: windowTop, behavior:'instant'});
	return problems;
}`
