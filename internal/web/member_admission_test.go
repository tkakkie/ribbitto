package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestPostPinAdmissionPostgreSQL(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "acme", "secret channel")
			outsider := conversationtest.OrganizationWithOwner(t, pool, "other", "other")
			sessions := identitypg.NewSessions(pool, time.Now, nil)
			token, _, err := sessions.Create(t.Context(), f.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			foreign, _, err := sessions.Create(t.Context(), outsider.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1, 0)
			s := postgresServices(t, pool, sessions, "", false)
			s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) { l.post = middleware.NewPostRateLimiter(func() time.Time { return now }) })
			s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) { l.pin = middleware.NewPinRateLimiter(func() time.Time { return now }) })
			cat, err := i18n.New(slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewHandler("", cat, s)
			if err != nil {
				t.Fatal(err)
			}
			request := func(path, cookie, body string, want int, retry string) {
				t.Helper()
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if hx {
					r.Header.Set("HX-Request", "true")
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body.String())
				}
				if want == 429 && (w.Body.Len() != 0 || w.Header().Get("Retry-After") != retry || w.Header().Get("Location") != "" || w.Header().Get("HX-Redirect") != "") {
					t.Fatalf("refusal: %v %q", w.Header(), w.Body.String())
				}
				if want == 404 && w.Body.String() != "404 page not found\n" {
					t.Fatal("non-member saw organisation data")
				}
			}
			success := 303
			if hx {
				success = 200
			}
			path := view.ChannelURL("acme", f.Channel.ID)
			form := func(i int) string {
				return url.Values{"body": {"secret text"}, "idempotency_key": {fmt.Sprintf("%08x-0000-0000-0000-000000000001", i)}}.Encode()
			}
			for i := 1; i <= 20; i++ {
				target := path
				if i%2 == 0 {
					target = view.ConversationURL("acme", f.Channel.ID, &f.Channel.DefaultTopicID)
				}
				request(target, token, form(i), success, "")
			}
			request(path, token, form(21), 429, "1")
			request(path, token, form(1), 429, "1") // A committed replay still needs admission.
			request(path, foreign, form(21), 404, "")
			count := func(table string, want int) {
				t.Helper()
				var n int
				if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != want {
					t.Fatalf("%s: %d want %d", table, n, want)
				}
			}
			count("message", 20)
			count("event_log", 20)
			now = now.Add(time.Second)
			request(path, token, form(21), success, "") // Retry the refused key.
			now = now.Add(time.Second)
			request(path, token, form(21), success, "") // Committed replay consumes the refill.
			request(path, token, form(22), 429, "1")
			count("message", 21)
			count("event_log", 21)
			var message kernel.ID
			if err := pool.QueryRow(t.Context(), "SELECT id FROM message LIMIT 1").Scan(&message); err != nil {
				t.Fatal(err)
			}
			pin := view.PinURL("acme", f.Channel.ID, message)
			for i := 0; i < 10; i++ {
				action := "/pin"
				if i%2 == 1 {
					action = "/unpin"
				}
				request(pin+action, token, "", success, "")
			}
			request(pin+"/pin", token, "", 429, "2")
			request(pin+"/unpin", foreign, "", 404, "")
			count("event_log", 31)
			var pinned bool
			if err := pool.QueryRow(t.Context(), "SELECT pinned_at IS NOT NULL FROM message WHERE id=$1", message).Scan(&pinned); err != nil {
				t.Fatal(err)
			}
			if pinned {
				t.Fatal("refused pin wrote state")
			}
		})
	}
}

func TestPostPinAdmissionScope(t *testing.T) {
	member := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	for _, pin := range []bool{false, true} {
		for _, scope := range []string{"member", "organisation"} {
			t.Run(fmt.Sprintf("pin=%t/%s", pin, scope), func(t *testing.T) {
				other := member
				if scope == "member" {
					other.Member.ID = kernel.ID{3}
				} else {
					other.Organization.ID = kernel.ID{4}
				}
				now := time.Unix(1, 0)
				p := channelPages{channels: &fakeChannels{}, postLimit: middleware.NewPostRateLimiter(func() time.Time { return now }), pinLimit: middleware.NewPinRateLimiter(func() time.Time { return now }), pinning: &fakePinning{}}
				burst, interval := 20, time.Second
				if pin {
					burst, interval = 10, 2*time.Second
				}
				request := func(m org.Membership, want int) {
					t.Helper()
					body := &readRecorder{}
					r := httptest.NewRequest("POST", "/", body)
					r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
					r.SetPathValue("messageID", "02000000-0000-0000-0000-000000000000")
					w := httptest.NewRecorder()
					if pin {
						p.pin(w, r, m)
					} else {
						p.post(w, r, m, kernel.ID{9})
					} // Missing channel: no parsing.
					if w.Code != want || body.reads != 0 {
						t.Fatalf("status %d want %d; body reads %d", w.Code, want, body.reads)
					}
				}
				accepted := 404
				if pin {
					accepted = 303
				}
				for range burst {
					request(member, accepted)
				}
				request(member, 429)
				for range burst {
					request(other, accepted)
				}
				request(other, 429)
				request(member, 429)
				now = now.Add(interval - time.Nanosecond)
				request(member, 429)
				now = now.Add(time.Nanosecond)
				request(member, accepted)
				request(member, 429)
			})
		}
	}
}

func TestPostAdmissionSlots(t *testing.T) {
	for _, mode := range []string{"success", "validation", "parse", "lookup404", "lookupError", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			limits := middleware.NewPostRateLimiter(func() time.Time { return time.Unix(1, 0) })
			p := channelPages{postLimit: limits, channels: &fakeChannels{}, posting: testPoster()}
			body := "body=hello"
			want := 303
			switch mode {
			case "validation":
				body = "body="
				want = 422
			case "parse":
				body = "body=%zz"
				want = 400
			case "lookup404":
				p.channels = &fakeChannels{getErr: conversation.ErrChannelNotFound}
				want = 404
			case "lookupError":
				p.channels = &fakeChannels{getErr: errors.New("offline")}
				want = 500
			case "error":
				p.posting = testPosting(&fakePostingWriter{err: errors.New("offline")})
				want = 500
			case "panic":
				p.channels = nil
			}
			var held []func()
			for range 3 {
				release, ok := limits.Admit(kernel.ID{}, kernel.ID{})
				if !ok {
					t.Fatal("cannot hold slot")
				}
				held = append(held, release)
			}
			defer func() {
				for _, release := range held {
					release()
				}
			}()
			for range 2 { // Each exit must free the fourth slot immediately.
				func() {
					defer func() {
						if v := recover(); v != nil && mode != "panic" {
							panic(v)
						}
					}()
					r := httptest.NewRequest("POST", "/", strings.NewReader(body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("HX-Request", "true")
					w := httptest.NewRecorder()
					p.post(w, r, org.Membership{}, kernel.ID{1})
					if mode == "success" {
						want = 200
					}
					if mode == "panic" || w.Code != want {
						t.Fatalf("status %d want %d", w.Code, want)
					}
				}()
			}
			release, ok := limits.Admit(kernel.ID{}, kernel.ID{})
			if !ok {
				t.Fatal("slot leaked")
			}
			defer release()
			if unexpected, ok := limits.Admit(kernel.ID{}, kernel.ID{}); ok {
				unexpected()
				t.Fatal("fifth concurrent post admitted")
			}
		})
	}
}
