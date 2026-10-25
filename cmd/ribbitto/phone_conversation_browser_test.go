package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestPhoneConversationBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "phone conversation checks")
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "phone", strings.TrimSpace(strings.Repeat("long channel ", 5)))
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, strings.Repeat("LongTopicName", 6))
	account := identitytest.Account(t, pool, "phone-reader@example.org", "Reader")
	orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "reader", 1)
	handler, _, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
	acceptanceOK(t, err)
	feed, topicURL := view.ChannelURL("phone", f.Channel.ID), view.ConversationURL("phone", f.Channel.ID, &topic.ID)
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (r.URL.Path == feed || r.URL.Path == topicURL) {
			posts.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	owner, reader := sidebarBrowser(t, pool, server, f.AccountID), sidebarBrowser(t, pool, server, account)
	body := "https://example.org/" + strings.Repeat("longpath", 60) + "\nconst value = " + strings.Repeat("code_without_spaces", 30) + "\n" + strings.Repeat("日本語の長い文章", 60) + "\n" + strings.Repeat("EnglishWithoutSpaces", 30)
	post(t, owner, topicURL, body)
	page, closePage := boundedPage(t, browser, ctx, "phone conversation")
	defer closePage()
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	for _, cookie := range reader.client.Jar.Cookies(u) {
		page.MustSetCookies(&proto.NetworkCookieParam{Name: cookie.Name, Value: cookie.Value, URL: server.URL, Path: "/", Secure: true})
	}
	// Retain unread state across layout cases; restore visibility for real sending.
	page.MustEvalOnNewDocument(`window.phoneVisibility = 'hidden'; Object.defineProperty(document, 'visibilityState', {get: () => window.phoneVisibility});
		window.composerSubmits = 0; document.addEventListener('submit', e => { if (e.target.id === 'message-composer') window.composerSubmits++; }, true);`)
	insets := func(bottom int) {
		t.Helper()
		_, err := page.Call(ctx, string(page.SessionID), "Emulation.setSafeAreaInsetsOverride", map[string]any{"insets": map[string]int{"bottom": bottom}})
		acceptanceOK(t, err)
	}
	for _, size := range [][2]int{{390, 844}, {320, 568}, {1280, 800}} {
		for _, path := range []string{feed, topicURL} {
			t.Run(fmt.Sprintf("%d/%s", size[0], strings.TrimPrefix(path, feed)), func(t *testing.T) {
				phone := size[0] < 852
				bottom := 0
				if phone {
					bottom = 34
				}
				insets(bottom)
				page.MustSetViewport(size[0], size[1], 1, false)
				page.MustNavigate(server.URL + path).MustWaitLoad()
				controls := map[string]string{"header > a": "Back to lilypads", "#read-form button": "Mark all as read", "#panel-control-members": "Members", "#panel-control-overview": "lilypad overview"}
				if path == topicURL {
					controls["header a:not([aria-label])"] = f.Channel.Name
				}
				named := func(selector, name string) {
					t.Helper()
					element := page.MustElement(selector)
					tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: element.Object.ObjectID}).Call(page)
					acceptanceOK(t, err)
					if len(tree.Nodes) == 0 || tree.Nodes[0].Ignored || tree.Nodes[0].Name == nil || tree.Nodes[0].Name.Value.Str() != name {
						names := make([]string, len(tree.Nodes))
						for i, node := range tree.Nodes {
							names[i] = "<missing name>"
							if node.Name != nil {
								names[i] = node.Name.Value.Str()
							}
						}
						t.Fatalf("%s accessible name: want %q, got %q", selector, name, names)
					}
				}
				for selector, name := range controls {
					// Below desk Mark all as read is in the overview sheet (#972).
					if !phone && selector == "header > a" || phone && selector == "#read-form button" {
						continue
					}
					named("#conversation "+selector, name)
				}
				if phone {
					page.MustNavigate(server.URL + path + "/overview").MustWaitLoad()
					named(`#side-panel button[form="read-form"]`, "Mark all as read")
					page.MustNavigate(server.URL + path).MustWaitLoad()
				}
				if problem := page.MustEval(`phone => {
					const header = document.querySelector('#conversation > header'), box = e => e.getBoundingClientRect(), width = document.documentElement.clientWidth;
					const conversation = box(document.getElementById('conversation'));
					if (phone && (conversation.left !== 0 || conversation.right !== width)) return 'conversation edges: want 0 and ' + width + ', got ' + conversation.left + ' and ' + conversation.right;
					const controls = [...header.querySelectorAll('a, button')].filter(e => e.checkVisibility());
					if (phone && controls.some(e => box(e).width < 44 || box(e).height < 44 || box(e).left < 0 || box(e).right > width)) return 'header target under 44 x 44 or outside viewport';
					const image = header.querySelector('img'), heading = header.querySelector('h1');
					if (!image.checkVisibility() || !image.naturalWidth) return 'no channel image';
					if (phone && [...controls, image, heading].some(e => Math.abs((box(e).top + box(e).bottom) / 2 - (box(header).top + box(header).bottom) / 2) > 1)) return 'header is not one row';
					const name = heading.querySelector('span') || heading;
					if (phone && (getComputedStyle(name).textOverflow !== 'ellipsis' || getComputedStyle(name).whiteSpace !== 'nowrap' || name.scrollWidth <= name.clientWidth || box(name).height > 22)) return 'name does not truncate on one line';
					const channel = header.querySelector('a:not([aria-label]) span');
					if (phone && channel && (channel.scrollWidth <= channel.clientWidth || getComputedStyle(channel).textOverflow !== 'ellipsis')) return 'channel breadcrumb does not truncate';
					for (const selector of ['#panel-control-members span', '#read-form button span']) {
						if (document.querySelector(selector).checkVisibility() === phone) return 'header label visibility changed: ' + selector;
					}
					if (!phone && header.querySelector(':scope > a').checkVisibility()) return 'desktop back link shown';
					if (phone && document.querySelector('#read-form button').checkVisibility()) return 'phone header shows Mark all as read';
					if (document.documentElement.scrollWidth > width || [...document.querySelectorAll('#message-list, #message-items, #message-items li, #message-items p')].some(e => e.scrollWidth > e.clientWidth)) return 'sideways scroll';
					const items = box(document.getElementById('message-items')), list = box(document.getElementById('message-list'));
					if (phone && (items.width !== document.getElementById('message-list').clientWidth || getComputedStyle(document.getElementById('message-items')).paddingLeft !== '16px' || items.left !== list.left)) return 'message width or 16 px gutter';
					const composer = document.getElementById('message-composer'), send = composer.querySelector('button'), input = document.getElementById('message-body');
					const wrapperStyle = getComputedStyle(composer.parentElement);
					if (phone && (wrapperStyle.paddingLeft !== '16px' || wrapperStyle.paddingRight !== '16px')) return 'composer side padding: want 16px, got ' + wrapperStyle.paddingLeft + ' and ' + wrapperStyle.paddingRight;
					if (phone && (parseFloat(getComputedStyle(input).fontSize) < 16 || box(send).width < 44 || box(send).height < 44)) return 'composer font or send target too small';
					if (phone && (getComputedStyle(composer.parentElement).paddingBottom !== '50px' || box(composer).bottom > document.documentElement.clientHeight - 34)) return 'composer below safe area';
					if (composer.getAttribute('hx-post') !== location.pathname) return 'latest submission was not enhanced to its own feed or topic';
					if (phone) {
						const checkbox = document.querySelector('#message-items input[type="checkbox"]');
						checkbox.checked = true;
						try {
							const branchForm = document.getElementById('branch-form'), style = getComputedStyle(branchForm);
							if (!branchForm.checkVisibility()) return 'move form hidden while a message is checked';
							if (style.paddingLeft !== '16px' || style.paddingRight !== '16px') return 'move form side padding: want 16px, got ' + style.paddingLeft + ' and ' + style.paddingRight;
						} finally {
							checkbox.checked = false;
						}
					}
					return '';
				}`, phone).Str(); problem != "" {
					t.Fatal(problem)
				}
			})
		}
	}
	for _, path := range []string{feed, topicURL} {
		page.MustNavigate(server.URL + path + "?before=3").MustWaitLoad()
		if page.MustElement("#message-composer").MustAttribute("hx-post") != nil {
			t.Fatalf("%s history submission was enhanced", path)
		}
	}
	for _, size := range [][2]int{{390, 844}, {320, 568}} {
		page.MustSetViewport(size[0], size[1], 1, false)
		insets(34)
		page.MustNavigate(server.URL + feed).MustWaitLoad()
		before := posts.Load()
		for _, mode := range []string{"composing", "229", "ended"} {
			// Each path must stand alone, beyond the previous completion guard.
			page.MustEval(`() => new Promise(resolve => setTimeout(resolve, 120))`)
			if submits := page.MustEval(`mode => {
				const body = document.getElementById('message-body'); body.focus(); body.value = '日本語変換';
				body.dispatchEvent(new CompositionEvent('compositionstart', {bubbles:true}));
				body.dispatchEvent(new InputEvent('input', {bubbles:true, isComposing:true}));
				if (mode === 'ended') body.dispatchEvent(new CompositionEvent('compositionend', {bubbles:true}));
				body.dispatchEvent(new KeyboardEvent('keydown', {bubbles:true, cancelable:true, key:'Enter', isComposing:mode === 'composing', keyCode:mode === '229' ? 229 : 13}));
				if (mode !== 'ended') body.dispatchEvent(new CompositionEvent('compositionend', {bubbles:true}));
				return window.composerSubmits;
			}`, mode).Int(); submits != 0 {
				t.Fatalf("%d px IME %s Enter submitted %d times", size[0], mode, submits)
			}
		}
		page.MustEval(`() => { window.phoneVisibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); return new Promise(resolve => setTimeout(resolve, 120)); }`)
		if posts.Load() != before {
			t.Fatal("IME Enter sent a message")
		}
		text := fmt.Sprintf("phone sent %d", size[0])
		inputStep(t, page, fmt.Sprintf("%d px typing", size[0]), func(p *rod.Page) { p.MustElement("#message-body").MustSelectAllText().MustInput(text) })
		inputStep(t, page, fmt.Sprintf("%d px Enter", size[0]), func(p *rod.Page) { p.Keyboard.MustType(input.Enter) })
		if err := page.Timeout(5 * time.Second).Wait(rod.Eval(`text => [...document.querySelectorAll('#message-items p')].some(e => e.textContent === text) && document.getElementById('message-body').value === '' && document.activeElement.id === 'message-body'`, text)); err != nil {
			t.Fatalf("%d px send did not show, clear and retain focus: %v", size[0], err)
		}
		if posts.Load() != before+1 {
			t.Fatal("send did not post exactly once")
		}
	}
}
