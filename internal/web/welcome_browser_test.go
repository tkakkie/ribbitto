package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testWelcomeBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const css = "/static/css/app.css"
	invalid := map[string]string{}
	for _, name := range []string{"token", "organization_name", "slug", "display_name", "handle", "email", "password"} {
		invalid[name] = "setup.error." + name
	}
	cases := []struct {
		name           string
		page           templ.Component
		fields, alerts int
	}{
		{"setup", view.Setup(css, view.SetupForm{}), 7, 0},
		{"setup-invalid", view.Setup(css, view.SetupForm{Errors: invalid}), 7, 7},
		{"signin", view.SignIn(css, view.SignInForm{SignUpOpen: true}), 2, 0},
		{"signin-failed", view.SignIn(css, view.SignInForm{SignUpOpen: true, Error: "signin.error.invalid"}), 2, 1},
		{"signup", view.SignUp(css, view.SignUpForm{}), 4, 0},
		{"signup-invalid", view.SignUp(css, view.SignUpForm{Errors: invalid}), 4, 4},
		{"home", view.Hello(css, view.Viewer{SignUpOpen: true}), 0, 0},
		{"home-signed-in", view.Hello(css, view.Viewer{SignedIn: true, DisplayName: "Alice"}), 0, 0},
		{"limit", view.TooManyRequests(css), 0, 0},
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	for _, c := range cases {
		mux.Handle("/"+c.name, catalogues.Middleware(templ.Handler(c.page)))
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	for _, size := range [][2]int{{390, 844}, {320, 568}, {1280, 800}} {
		phone := size[0] < 852
		insets := map[string]int{"left": 0, "right": 0, "bottom": 0}
		if phone {
			insets = map[string]int{"left": 20, "right": 20, "bottom": 34}
		}
		_, err := page.Call(ctx, string(page.SessionID), "Emulation.setSafeAreaInsetsOverride", map[string]any{"insets": insets})
		if err != nil {
			t.Fatal(err)
		}
		page.MustSetViewport(size[0], size[1], 1, false)
		for _, lang := range []string{"en", "ja"} {
			page.MustSetExtraHeaders("Accept-Language", lang)
			for _, theme := range []string{"light", "dark"} {
				if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: theme}}}).Call(page); err != nil {
					t.Fatal(err)
				}
				for _, c := range cases {
					t.Run(fmt.Sprintf("%d/%s/%s/%s", size[0], lang, theme, c.name), func(t *testing.T) {
						page.MustNavigate(server.URL + "/" + c.name).MustWaitLoad()
						if problem := page.MustEval(`(phone, lang, fields, alerts) => {
							const root = document.documentElement, box = e => e.getBoundingClientRect();
							const main = document.querySelector('main'), card = main.firstElementChild, brand = card.firstElementChild, content = card.lastElementChild;
							const composition = brand.querySelector('[aria-hidden]'), logo = brand.querySelector('img');
							if (root.lang !== lang) return 'wrong language';
							if (!brand.checkVisibility() || !logo.checkVisibility() || !logo.naturalWidth || brand.querySelectorAll('p').length !== 2 || [...brand.querySelectorAll('p')].some(e => !e.checkVisibility() || !e.textContent.trim())) return 'brand strip missing logo, name or tagline';
							if (composition.checkVisibility() === phone) return 'composition visibility';
							const probe = document.createElement('div'); probe.style.backgroundColor = 'var(--rb-selected)'; brand.append(probe);
							const tint = getComputedStyle(probe).backgroundColor; probe.remove();
							if (getComputedStyle(brand).backgroundColor !== tint) return 'brand tint';
							if (phone && (box(brand).height > 192 || box(brand).bottom > box(content).top)) return 'brand is not a short strip above content';
							if (!phone && (box(brand).right !== box(content).left || box(brand).top !== box(content).top || getComputedStyle(brand).paddingTop !== '48px' || composition.querySelectorAll('img').length !== 4)) return 'desktop welcome columns changed';
							const inputs = [...document.querySelectorAll('input:not([type=hidden])')], buttons = [...document.querySelectorAll('button')];
							if (inputs.length !== fields || buttons.length !== (fields > 0 || location.pathname === '/home-signed-in' ? 1 : 0) || document.querySelectorAll('[role=alert]').length !== alerts) return 'missing fields or error state';
							if ([...inputs, ...buttons].some(e => phone ? box(e).height < 48 : box(e).height !== 44)) return 'control height differs from phone minimum or desktop size';
							if (inputs.some(e => parseFloat(getComputedStyle(e).fontSize) !== (phone ? 16 : 14))) return 'field text size';
							if (location.pathname.startsWith('/signin')) {
								if ([document.querySelector('h1'), inputs[0]].some(e => box(e).top < 0 || box(e).bottom > root.clientHeight)) return 'sign-in heading or first field below first screen';
							}
							if (root.scrollWidth > root.clientWidth || [...inputs, ...buttons].some(e => box(e).left < 0 || box(e).right > root.clientWidth)) return 'sideways scroll or control outside viewport';
							if (phone && (!document.querySelector('meta[name=viewport]').content.includes('viewport-fit=cover') || getComputedStyle(main).paddingBottom !== '50px' || box(card).left < 36 || box(card).right > root.clientWidth - 36)) return 'welcome safe area';
							return '';
						}`, phone, lang, c.fields, c.alerts).Str(); problem != "" {
							t.Fatal(problem)
						}
						for _, input := range page.MustElements("input:not([type=hidden])") {
							tree, err := (proto.AccessibilityGetPartialAXTree{ObjectID: input.Object.ObjectID}).Call(page)
							if err != nil {
								t.Fatal(err)
							}
							if len(tree.Nodes) == 0 || tree.Nodes[0].Ignored || tree.Nodes[0].Name == nil || tree.Nodes[0].Name.Value.Str() == "" {
								t.Fatal("field has no accessible name")
							}
							if c.alerts > 1 && (tree.Nodes[0].Description == nil || tree.Nodes[0].Description.Value.Str() == "") {
								t.Fatal("invalid field has no accessible error description")
							}
						}
					})
				}
			}
		}
	}
}
