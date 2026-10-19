package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

type preferenceMemory struct{}

func (*preferenceMemory) SetSendKey(context.Context, kernel.ID, identity.SendKey) error { return nil }

type sendKeySession struct {
	oneSession
	key identity.SendKey
}

func (s sendKeySession) Resolve(ctx context.Context, token string) (identity.Account, identity.Session, error) {
	a, session, err := s.oneSession.Resolve(ctx, token)
	a.SendKey = s.key
	return a, session, err
}

func TestSendKeyAuthorization(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "General")
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "Other")
	b := identitytest.Account(t, pool, "b@example.org", "B")
	orgtest.Member(t, pool, other.OrganizationID, b, org.RoleMember, "bob-other", 1)
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	token, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	bToken, _, err := sessions.Create(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	path := "/organizations/acme/send-key"
	channel := strings.TrimPrefix(view.ChannelURL("acme", f.Channel.ID), "/organizations/acme/channels/")
	topic := strings.TrimPrefix(view.ConversationURL("acme", f.Channel.ID, &f.Channel.DefaultTopicID), view.ChannelURL("acme", f.Channel.ID)+"/topics/")
	for _, sameOrg := range []bool{false, true} {
		if sameOrg {
			orgtest.Member(t, pool, f.OrganizationID, b, org.RoleMember, "bob", 1)
		}
		for _, tc := range []struct {
			name, cookie, slug, value, channel, topic, target string
			status                                            int
		}{
			{"feed", token, "acme", "modifier_enter", channel, "", view.ChannelURL("acme", f.Channel.ID), 303},
			{"topic", token, "acme", "modifier_enter", channel, topic, view.ConversationURL("acme", f.Channel.ID, &f.Channel.DefaultTopicID), 303},
			{"list", token, "acme", "modifier_enter", "", "", "/organizations/acme/channels", 303},
			{"hostile IDs", token, "acme", "modifier_enter", "https://evil.example/", "//evil.example/", "/organizations/acme/channels", 303},
			{"invalid topic", token, "acme", "modifier_enter", channel, "invalid", view.ChannelURL("acme", f.Channel.ID), 303},
			{"invalid value", token, "acme", "bogus", channel, topic, "", 400},
			{"signed out", "", "acme", "modifier_enter", channel, topic, "", 404},
			{"non-member", token, "other", "modifier_enter", channel, topic, "", 404},
			{"other member", bToken, "other", "modifier_enter", "", "", "/organizations/other/channels", 303},
		} {
			t.Run(fmt.Sprintf("same-org=%t/%s", sameOrg, tc.name), func(t *testing.T) {
				if _, err := pool.Exec(t.Context(), "UPDATE account SET send_key = 'enter'"); err != nil {
					t.Fatal(err)
				}
				h, err := NewHandler("", cat, postgresServices(t, pool, sessions, "", false))
				if err != nil {
					t.Fatal(err)
				}
				body := url.Values{"send_key": {tc.value}, "account_id": {fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])}, "return_channel": {tc.channel}, "return_topic": {tc.topic}, "redirect": {"https://evil.example/"}, "before": {"42"}, "panel": {"members"}}
				w := serveForm(h, "POST", strings.Replace(path, "acme", tc.slug, 1), tc.cookie, body)
				if w.Code != tc.status || w.Header().Get("Location") != tc.target {
					t.Fatalf("status/redirect: %d %q", w.Code, w.Header().Get("Location"))
				}
				for _, id := range []kernel.ID{f.AccountID, b} {
					var got string
					if err := pool.QueryRow(t.Context(), "SELECT send_key FROM account WHERE id=$1", id).Scan(&got); err != nil {
						t.Fatal(err)
					}
					want := "enter"
					if tc.status == 303 && ((id == f.AccountID && tc.cookie == token) || (id == b && tc.cookie == bToken)) {
						want = "modifier_enter"
					}
					if got != want {
						t.Fatalf("account %v preference %q, want %q", id, got, want)
					}
				}
			})
		}
	}
	t.Run("rate limit writes nothing", func(t *testing.T) {
		services := postgresServices(t, pool, sessions, "", false)
		now := time.Now()
		services.SendKeyLimits = middleware.NewMemberRateLimiterWithBudget(func() time.Time { return now }, 1, 1, 0)
		h, err := NewHandler("", cat, services)
		if err != nil {
			t.Fatal(err)
		}
		for i, value := range []string{"modifier_enter", "enter"} {
			w := serveForm(h, "POST", path, token, url.Values{"send_key": {value}})
			want := 303
			if i == 1 {
				want = 429
			}
			if w.Code != want {
				t.Fatalf("request %d: status %d, want %d", i, w.Code, want)
			}
		}
		var got string
		if err := pool.QueryRow(t.Context(), "SELECT send_key FROM account WHERE id=$1", f.AccountID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "modifier_enter" {
			t.Fatal("limited request changed preference")
		}
	})
}

func TestComposerSendKeyRendering(t *testing.T) {
	for _, key := range []identity.SendKey{identity.SendKeyEnter, identity.SendKeyModifierEnter} {
		for _, response := range []string{"page", "200", "422"} {
			t.Run(string(key)+"/"+response, func(t *testing.T) {
				h, _, _, place := composerHandler(t, false, func(s *Services) { s.Sessions = sendKeySession{key: key} })
				path := view.ChannelURL("acme", place.Channel)
				w := serveForm(h, "GET", path, "live", nil)
				if response != "page" {
					_, values := composerForm(t, w)
					values.Set("body", "sent")
					if response == "422" {
						values.Set("body", "\n")
					}
					w = composerRequest(h, path, values, true)
				}
				wantStatus := 200
				if response == "422" {
					wantStatus = 422
				}
				body := w.Body.String()
				help := "Enter sends;"
				if key == identity.SendKeyModifierEnter {
					help = "Ctrl+Enter"
				}
				if w.Code != wantStatus || !strings.Contains(body, `data-send-key="`+string(key)+`"`) || strings.Contains(body, "enterkeyhint") {
					t.Fatalf("status/mode/key label: %d %s", w.Code, body)
				}
				doc, err := html.Parse(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				var helpMarkup strings.Builder
				for n := range doc.Descendants() {
					if attr(n, "id") == "message-help" {
						if err := html.Render(&helpMarkup, n); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
				if response == "422" {
					if !strings.Contains(helpMarkup.String(), `role="alert"`) || strings.Contains(helpMarkup.String(), help) {
						t.Fatal("422 alert did not take precedence")
					}
				} else if !strings.Contains(helpMarkup.String(), help) {
					t.Fatal("missing shortcut help")
				}
				if response == "page" && !strings.Contains(body, `value="`+string(key)+`" checked`) {
					t.Fatal("current radio not checked")
				}
			})
		}
	}
}
