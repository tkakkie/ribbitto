package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestBranchAdmissionPostgreSQL(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprintf("hx=%t", hx), func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			destination := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "design")
			sessions := identitypg.NewSessions(pool, time.Now, nil)
			token, _, err := sessions.Create(t.Context(), f.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			services := postgresServices(t, pool, sessions, "", false)
			services.MemberLimits = memberLimitOverride(services.MemberLimits, func(l *MemberLimits) {
				l.branch = middleware.NewBranchRateLimiter(func() time.Time { return time.Unix(1, 0) })
			})
			m, err := services.Authz.Member(t.Context(), &identity.Account{ID: f.AccountID}, "acme")
			if err != nil {
				t.Fatal(err)
			}
			message, err := fixturePoster(pool).Post(t.Context(), m, f.Channel.ID, "moves repeatedly")
			if err != nil {
				t.Fatal(err)
			}
			catalogues, err := i18n.New(slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler("", catalogues, services)
			if err != nil {
				t.Fatal(err)
			}
			uuid := func(id kernel.ID) string {
				return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
			}
			from, to := f.Channel.DefaultTopicID, destination.ID
			for i := range 21 {
				form := url.Values{"message": {uuid(message.ID)}, "from": {uuid(from)}, "to": {uuid(to)}}
				r := httptest.NewRequest("POST", view.ChannelURL("acme", f.Channel.ID)+"/branch", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if hx {
					r.Header.Set("HX-Request", "true")
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want, redirect := http.StatusSeeOther, "Location"
				if hx {
					want, redirect = http.StatusOK, "HX-Redirect"
				}
				if i == 20 {
					want = http.StatusTooManyRequests
				}
				if w.Code != want {
					t.Errorf("branch request %d: status %d, want %d", i+1, w.Code, want)
				}
				if i < 20 {
					if w.Header().Get(redirect) != view.ConversationURL("acme", f.Channel.ID, &to) {
						t.Fatal("successful branch lost destination redirect")
					}
					from, to = to, from
				} else if w.Body.Len() != 0 || w.Header().Get("Retry-After") != "6" || w.Header().Get("Location") != "" || w.Header().Get("HX-Redirect") != "" {
					t.Errorf("refusal: headers %v, body %q", w.Header(), w.Body.String())
				}
			}
			var topic kernel.ID
			var moves int
			if err := pool.QueryRow(t.Context(), "SELECT topic_id FROM message WHERE id = $1", message.ID).Scan(&topic); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM event_log WHERE kind = 'messages.moved'").Scan(&moves); err != nil {
				t.Fatal(err)
			}
			if topic != from || moves != 20 {
				t.Errorf("after refusal: topic %x, want %x; messages.moved events %d, want 20", topic, from, moves)
			}
		})
	}
}

func TestBranchAdmissionScope(t *testing.T) {
	member := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	otherMember, otherOrganization := member, member
	otherMember.Member.ID = kernel.ID{3}
	otherOrganization.Organization.ID = kernel.ID{4}
	for _, tc := range []struct {
		name  string
		other org.Membership
	}{
		{"organisation alone differs", otherOrganization},
		{"member alone differs", otherMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1, 0)
			p := channelPages{branching: &fakeBrancher{}, branchLimit: middleware.NewBranchRateLimiter(func() time.Time { return now })}
			request := func(m org.Membership, want int) {
				t.Helper()
				// Malformed forms spend admission but cannot reach branching.
				r := httptest.NewRequest("POST", "/branch", strings.NewReader("name=%zz"))
				r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				p.branch(w, r, m)
				if w.Code != want {
					t.Fatalf("branch admission: status %d, want %d", w.Code, want)
				}
			}
			for range 20 {
				request(member, 400)
			}
			request(member, 429)
			for range 20 {
				request(tc.other, 400)
			}
			request(tc.other, 429)
			request(member, 429)
			now = now.Add(6*time.Second - time.Nanosecond)
			request(member, 429)
			now = now.Add(time.Nanosecond)
			request(member, 400)
			request(member, 429)
		})
	}
}
