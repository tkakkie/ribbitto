package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testMessageActionsPageBrowser(t *testing.T, browser *rod.Browser) {
	for _, width := range []int{1440, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
			topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "actions")
			message, err := fixturePoster(pool).PostToTopic(ctx, m, f.Channel.ID, &topic.ID, "actions page original")
			if err != nil {
				t.Fatal(err)
			}
			sessions := identitypg.NewSessions(pool, time.Now, nil)
			cookie, _, err := sessions.Create(ctx, f.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, postgresServices(t, pool, sessions, "", false))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "message actions page")
			defer closePage()
			page = page.WithPanic(failAtTestLine(t))
			page.MustSetViewport(width, 844, 1, width < 768)
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
				t.Fatal(err)
			}
			if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: width < 768}).Call(page); err != nil {
				t.Fatal(err)
			}
			permalink := view.MessageURL("acme", message.ID)
			actions := server.URL + permalink + "/actions"
			open := func() {
				if err := navigateLoaded(page, server.URL+permalink); err != nil {
					t.Fatal(err)
				}
				// Rod's Focus waits for animation frames, unavailable with scripts disabled.
				page.MustEval(`s => document.querySelector(s).focus()`, fmt.Sprintf("a#message-actions-%x", message.ID))
				page.Keyboard.MustType(input.Enter)
				page.MustWait(`url => location.href === url`, actions)
				if got := page.MustElement("main h1").MustText(); got != "Ribbit actions" {
					t.Fatal(got)
				}
				if page.MustEval(`() => document.querySelector('main img[src^="/static/brand/"]') !== null`).Bool() {
					t.Fatal("in-organisation actions page must not render the welcome hero")
				}
				if page.MustEval(`() => document.querySelectorAll('main').length !== 1 || document.querySelectorAll('h1').length !== 1 || document.title !== 'Ribbit actions · ribbitto'`).Bool() {
					t.Fatal("actions page lost its title, main landmark or heading")
				}
				if got := page.MustElement("main a").MustProperty("href").Str(); got != server.URL+permalink {
					t.Fatalf("copyable permalink = %q", got)
				}
				if page.MustElement("main a").MustText() != permalink {
					t.Fatal("permalink is not shown")
				}
				if page.MustEval(`() => document.documentElement.scrollWidth > innerWidth`).Bool() {
					t.Fatal("actions overflow")
				}
			}
			activate := func(selector string) {
				for range 7 {
					if page.MustEval(`s => document.activeElement.matches(s)`, selector).Bool() {
						page.Keyboard.MustType(input.Enter)
						return
					}
					page.Keyboard.MustType(input.Tab)
				}
				t.Fatalf("Tab did not reach %s", selector)
			}
			open()
			// Native Tab order includes Resolve on a named topic.
			for _, selector := range []string{"main a", "[name=quote]", "[name=reply]", "form[method=post] button", "button[name=message]", "main > div > a:last-child"} {
				page.Keyboard.MustType(input.Tab)
				if !page.MustEval(`s => document.activeElement.matches(s)`, selector).Bool() {
					t.Fatalf("Tab missed %s", selector)
				}
			}
			page.KeyActions().Press(input.ShiftLeft).Type(input.Tab, input.Tab).MustDo()
			if !page.MustEval(`() => document.activeElement.matches('form[method=post] button')`).Bool() {
				t.Fatal("Shift-Tab missed Pin")
			}
			page.Keyboard.MustType(input.Enter)
			page.MustElement("#message-items").MustWaitVisible()
			pinned := func(want bool) {
				t.Helper()
				var got bool
				if err := pool.QueryRow(ctx, "SELECT pinned_at IS NOT NULL FROM message WHERE id = $1", message.ID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("pinned = %t, want %t", got, want)
				}
			}
			pinned(true)
			open()
			if page.MustElement("form[method=post] button").MustText() != "Unpin" {
				t.Fatal("missing Unpin")
			}
			activate("form[method=post] button")
			page.MustElement("#message-items").MustWaitVisible()
			pinned(false)
			open()
			activate("[name=quote]")
			page.MustElement("#composer-quote input[name=quote]")
			if got := page.MustElement("#composer-quote input[name=quote]").MustProperty("value").Str(); got != realtime.FormatPayloadID(message.ID) {
				t.Fatalf("quote = %q", got)
			}
			open()
			activate("main > div > a:last-child")
			page.MustElement("#" + view.MessageDOMID(message.ID)).MustWaitVisible()
		})
	}
}
