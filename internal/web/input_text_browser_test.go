package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Below desk every visible text input, textarea and select has at least 16 px text,
// since iOS Safari zooms the page when a smaller field takes focus (#1120).
// Desktop keeps its sizes.
func testInputTextBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const css = "/static/css/app.css"
	conversation := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:     view.Channel{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: "general"},
		Channels:    []view.Channel{{ID: kernel.ID{1}, DefaultTopicID: kernel.ID{2}, Name: "general"}},
		DisplayName: "Reader", Handle: "reader", Role: "member", ImageKeys: []string{"lake", "flowers", "moon", "kite"},
		Messages: []view.Message{{ID: kernel.ID{9}, MemberID: kernel.ID{3}, TopicID: kernel.ID{2}, EventSeq: 1,
			DisplayName: "Writer", Handle: "writer", CreatedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), Body: "hello", DefaultTopic: true}}}
	list := conversation
	list.List = true
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	for path, page := range map[string]templ.Component{
		"/list":         view.ChannelListScreen(css, list),
		"/conversation": view.ChannelScreen(css, conversation),
		"/setup":        view.Setup(css, view.SetupForm{}),
		"/signin":       view.SignIn(css, view.SignInForm{SignUpOpen: true}),
		"/signup":       view.SignUp(css, view.SignUpForm{}),
	} {
		mux.Handle("GET "+path, catalogues.Middleware(templ.Handler(page)))
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "input text")
	defer closeTab()

	// fields lists the visible text-like inputs, textareas and selects with their
	// computed font size; checkboxes, radios and buttons do not zoom.
	const fields = `() => [...document.querySelectorAll('input:not([type=hidden],[type=checkbox],[type=radio],[type=submit],[type=button],[type=reset],[type=image],[type=file],[type=range],[type=color]), textarea, select')]
		.filter(e => e.checkVisibility({visibilityProperty: true}))
		.map(e => ({ID: e.id, Size: parseFloat(getComputedStyle(e).fontSize)}))`
	type field struct {
		ID   string
		Size float64
	}
	for _, size := range [][2]int{{390, 844}, {1440, 900}} {
		phone := size[0] < 852
		for _, c := range []struct {
			name, path string
			// show reveals the fields under test: the create popover, or the
			// move form, which replaces the composer while a message is
			// selected.
			show string
			want []string
		}{
			{"list/create", "/list", `() => document.getElementById('channel-create').showPopover()`, []string{"channel-name"}},
			{"conversation/branch", "/conversation", `() => document.querySelector('#message-items input[type=checkbox]').click()`, []string{"branch-to", "branch-name"}},
			{"setup", "/setup", "", []string{"token", "organization_name", "slug", "display_name", "handle", "email", "password"}},
			{"signin", "/signin", "", []string{"email", "password"}},
			{"signup", "/signup", "", []string{"display_name", "handle", "email", "password"}},
		} {
			name := fmt.Sprintf("%dx%d/%s", size[0], size[1], c.name)
			got := func() []field {
				stepCtx, stepCancel := context.WithTimeout(ctx, 20*time.Second)
				defer stepCancel()
				page := tab.Context(stepCtx).WithPanic(func(v any) { t.Fatalf("%s: %v", name, v) })
				page.MustSetViewport(size[0], size[1], 1, false)
				page.MustNavigate(server.URL + c.path).MustWaitLoad()
				if c.show != "" {
					page.MustEval(c.show)
				}
				var got []field
				if err := page.MustEval(fields).Unmarshal(&got); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return got
			}()
			ids := make([]string, len(got))
			for i, f := range got {
				ids[i] = f.ID
			}
			if !slices.Equal(ids, c.want) {
				t.Fatalf("%s: visible text fields %q; want %q", name, ids, c.want)
			}
			for _, f := range got {
				if phone && f.Size < 16 {
					t.Errorf("%s: #%s text is %g px; want at least 16 px below desk", name, f.ID, f.Size)
				}
				// Desktop keeps text-body, as before #1120.
				if !phone && f.Size != 14 {
					t.Errorf("%s: #%s text is %g px; want desktop's 14 px", name, f.ID, f.Size)
				}
			}
		}
	}
}
