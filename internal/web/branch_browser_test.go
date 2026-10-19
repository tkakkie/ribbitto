package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// branchLayout is what a user sees of the move form and the composer area.
type branchLayout struct {
	Form, Composer      bool
	FormHeight          float64
	List                [2]float64
	Checkbox            float64
	Active, FormControl string
}

// The move form takes no space until a message is checked, then covers the
// composer's area without moving the list; with or without JavaScript (#923).
func testBranchFormBrowser(t *testing.T, browser *rod.Browser) {
	for _, js := range []bool{true, false} {
		t.Run(map[bool]string{true: "javascript", false: "no-javascript"}[js], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Branching = &fakeBrancher{}
				s.Messages = populatedMessages()
			}))
			if err != nil {
				t.Fatal(err)
			}
			posts := make(chan url.Values, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				if r.Method == "POST" {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
						return
					}
					if (r.Header.Get("HX-Request") != "") != js {
						t.Errorf("enhanced = %t with JavaScript %t", !js, js)
					}
				}
				handler.ServeHTTP(w, r)
				if strings.HasSuffix(r.URL.Path, "/branch") {
					posts <- r.PostForm
				}
			}))
			defer server.Close()
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			page.MustSetViewport(1280, 800, 1, false)
			if !js {
				// As in testComposerWithoutJavaScript: block the external scripts,
				// since rod needs script execution for its own input.
				router := page.HijackRequests()
				router.MustAdd("*.js", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) })
				go router.Run()
				defer router.MustStop()
			}
			path := view.ChannelURL("acme", kernel.ID{1})
			page.MustNavigate(server.URL + path).MustWaitLoad()
			layout := func() branchLayout {
				t.Helper()
				var got branchLayout
				err := page.MustEval(`() => {
					const visible = id => document.getElementById(id).checkVisibility({visibilityProperty: true});
					const list = document.getElementById('message-list').getBoundingClientRect();
					return {
						Form: visible('branch-form'), Composer: visible('message-composer'),
						FormHeight: document.getElementById('branch-form').getBoundingClientRect().height,
						List: [list.top, list.height],
						Checkbox: document.querySelector('#message-items input[type=checkbox]').getBoundingClientRect().top,
						Active: document.activeElement.id,
						FormControl: document.activeElement.form?.id ?? '',
					};
				}`).Unmarshal(&got)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			checkbox := page.MustElement("#message-items input[type=checkbox]")
			// tabFrom presses Tab from the checkbox until focus leaves the list
			// and returns the first control it reaches after it.
			tabFrom := func() branchLayout {
				t.Helper()
				checkbox.MustFocus()
				for range 6 {
					page.Keyboard.MustType(input.Tab)
					if got := layout(); got.Active == "branch-to" || got.Active == "message-body" {
						return got
					}
				}
				t.Fatalf("Tab did not leave the list: %+v", layout())
				return branchLayout{}
			}
			before := layout()
			if before.Form || before.FormHeight != 0 || !before.Composer {
				t.Fatalf("nothing checked: %+v; want no move form and a visible composer", before)
			}
			if got := tabFrom(); got.Active != "message-body" {
				t.Fatalf("Tab reached %q with nothing checked; want the composer", got.Active)
			}
			page.MustElement("#message-body").MustInput("draft")
			checkbox.MustFocus()
			page.Keyboard.MustType(input.Space)
			checked := layout()
			t.Logf("move form %.0f px tall", checked.FormHeight)
			if !checked.Form || checked.FormHeight == 0 || checked.Composer || checked.Active != *checkbox.MustAttribute("id") {
				t.Fatalf("checked: %+v; want the move form instead of the composer, focus kept", checked)
			}
			if checked.List != before.List || checked.Checkbox != before.Checkbox {
				t.Fatalf("list or checkbox moved: %+v, before %+v", checked, before)
			}
			if got := tabFrom(); got.Active != "branch-to" || got.FormControl != "branch-form" {
				t.Fatalf("Tab reached %q while checked; want the move form", got.Active)
			}
			checkbox.MustFocus()
			page.Keyboard.MustType(input.Space)
			if got := layout(); got.Form || !got.Composer || page.MustElement("#message-body").MustProperty("value").Str() != "draft" {
				t.Fatalf("unchecked: %+v; want the composer back with its draft", got)
			}
			submit := func(name string, load bool) url.Values {
				t.Helper()
				wait := func() {}
				if load {
					wait = page.WaitNavigation(proto.PageLifecycleEventNameLoad)
				}
				page.MustElement("#branch-name").MustSelectAllText().MustInput(name)
				page.Keyboard.MustType(input.Enter)
				wait()
				select {
				case values := <-posts:
					return values
				case <-ctx.Done():
					t.Fatal("the move form did not post", ctx.Err())
				}
				return nil
			}
			checkbox.MustFocus()
			page.Keyboard.MustType(input.Space)
			submit(strings.Repeat("x", 81), !js)
			page.MustElement(`#branch-feedback [role="alert"]`)
			if failed := layout(); !failed.Form || failed.Composer == js {
				t.Fatalf("after a failed move: %+v; want the form with its error, and the composer only without a selection", failed)
			}
			if !js {
				// A plain error page has nothing checked; the form still shows,
				// in the column above a usable composer.
				if page.MustEval(`() => document.querySelectorAll('#message-items input:checked').length`).Int() != 0 {
					t.Fatal("plain error page kept a selection")
				}
				page.MustElement("#branch-name").MustFocus()
				if got := layout(); got.Active != "branch-name" {
					t.Fatalf("focus %q; want the move form's name", got.Active)
				}
				checkbox = page.MustElement("#message-items input[type=checkbox]")
				checkbox.MustFocus()
				page.Keyboard.MustType(input.Space)
			}
			values := submit("design", true)
			if values.Get("name") != "design" || len(values["message"]) != 1 || !strings.HasPrefix(values.Get("message"), "08000000-") {
				t.Fatalf("posted %v; want the checked message and the new name", values)
			}
			if got := page.MustInfo().URL; got != server.URL+view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{0x32}) {
				t.Fatalf("moved to %s", got)
			}
		})
	}
}
