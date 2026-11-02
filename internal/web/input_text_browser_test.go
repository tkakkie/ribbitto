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
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Below desk or with a coarse pointer every visible text input, textarea and
// select has at least 16 px text, since iOS Safari zooms the page when a
// smaller field takes focus (#1120, #2081). Desktop with a fine pointer keeps
// its sizes.
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
	quote := conversation
	quote.Quote = &conversation.Messages[0]
	quote.QuoteText = &conversation.Messages[0].Body
	list := conversation
	list.List = true
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	for path, page := range map[string]templ.Component{
		"/list":         view.ChannelListScreen(css, list),
		"/conversation": view.ChannelScreen(css, conversation),
		"/quote":        view.ChannelScreen(css, quote),
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
	for _, v := range []struct {
		width, height int
		touch         bool
	}{
		{390, 844, false},
		{874, 402, true},
		{1440, 900, false},
	} {
		desk := v.width >= 852
		phone := !desk || v.touch
		for _, c := range []struct {
			name, path string
			// show reveals the fields under test: the create popover, or the
			// move form, which replaces the composer while a message is
			// selected.
			show string
			want []string
		}{
			{"list/create", "/list", `() => document.getElementById('channel-create').showPopover()`, []string{"channel-name"}},
			{"conversation", "/conversation", "", []string{"message-body"}},
			{"conversation/quote", "/quote", "", []string{"quote-text", "message-body"}},
			{"conversation/branch", "/conversation", `() => document.querySelector('#message-items input[type=checkbox]').click()`, []string{"branch-to", "branch-name"}},
			{"setup", "/setup", "", []string{"token", "organization_name", "slug", "display_name", "handle", "email", "password"}},
			{"signin", "/signin", "", []string{"email", "password"}},
			{"signup", "/signup", "", []string{"display_name", "handle", "email", "password"}},
		} {
			name := fmt.Sprintf("%dx%d/%s", v.width, v.height, c.name)
			got := func() []field {
				stepCtx, stepCancel := context.WithTimeout(ctx, 20*time.Second)
				defer stepCancel()
				page := tab.Context(stepCtx).WithPanic(func(v any) { t.Fatalf("%s: %v", name, v) })
				page.MustSetViewport(v.width, v.height, 1, v.touch)
				if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: v.touch}).Call(page); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				page.MustNavigate(server.URL + c.path).MustWaitLoad()
				// Headless Chrome on Linux CI has no mouse (pointer: none, hover: none),
				// so non-touch checks that the pointer is not coarse.
				if media := page.MustEval(`() => [matchMedia('(min-width: 53.25rem)').matches, matchMedia('(pointer: coarse)').matches, matchMedia('(hover: none)').matches]`).Arr(); media[0].Bool() != desk || media[1].Bool() != v.touch || (v.touch && !media[2].Bool()) {
					t.Fatalf("%s: desk=%v pointer coarse=%v hover none=%v; want desk=%t touch=%t", name, media[0], media[1], media[2], desk, v.touch)
				}
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
					t.Errorf("%s: #%s text is %g px; want at least 16 px on phone", name, f.ID, f.Size)
				}
				// Desktop keeps text-title (15 px) in the composer and text-body (14 px) elsewhere.
				want := 14.0
				if f.ID == view.MessageBodyID {
					want = 15
				}
				if !phone && f.Size != want {
					t.Errorf("%s: #%s text is %g px; want desktop's %g px", name, f.ID, f.Size, want)
				}
			}
		}
	}
}
