package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func TestSidebarResync(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	owner, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	outsider := identitytest.Account(t, pool, "outsider@example.org", "outsider")
	foreign, _, err := sessions.Create(t.Context(), outsider)
	if err != nil {
		t.Fatal(err)
	}
	changes := &unread.Changes{}
	own := changes.Watch(f.OrganizationID, f.MemberID)
	defer own.Release()
	other := changes.Watch(f.OrganizationID, kernel.ID{42})
	defer other.Release()
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	services := postgresServices(t, pool, sessions, "", false)
	services.SidebarResync = changes
	h, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cookie, origin string
		status               int
		revoked              bool
	}{
		{"member", owner, "", 204, false},
		{"non-member", foreign, "", 404, false},
		{"signed out", "", "", 404, false},
		{"CSRF", owner, "https://foreign.example", 403, false},
		{"revoked", owner, "", 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.revoked {
				if _, err := pool.Exec(t.Context(), "DELETE FROM member WHERE organization_id=$1 AND id=$2", f.OrganizationID, f.MemberID); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := own.Snapshot()
			r := httptest.NewRequest("POST", "/organizations/acme/sidebar/resync?member=forged", nil)
			r.Header.Set("Cookie", middleware.SessionCookie+"="+tc.cookie)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			after, _ := own.Snapshot()
			want := before
			if tc.status == 204 {
				want++
			}
			unrelated, _ := other.Snapshot()
			if w.Code != tc.status || after != want || unrelated != 0 || (w.Code == 204 && w.Body.Len() != 0) {
				t.Fatalf("resync: status=%d generation=%d other=%d body=%q; want status=%d generation=%d", w.Code, after, unrelated, w.Body.String(), tc.status, want)
			}
		})
	}
}

func TestSidebarResyncAdmission(t *testing.T) {
	now := time.Unix(1, 0)
	limits := middleware.NewSidebarResyncRateLimiter(func() time.Time { return now })
	changes := &unread.Changes{}
	member := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
	otherMember := member
	otherMember.Member.ID = kernel.ID{3}
	otherOrganization := member
	otherOrganization.Organization.ID = kernel.ID{4}
	members := []org.Membership{member, otherMember, otherOrganization}
	watches := make([]*unread.ChangeWatch, len(members))
	for i, m := range members {
		watches[i] = changes.Watch(m.Organization.ID, m.Member.ID)
		defer watches[i].Release()
	}
	handle := resyncSidebar(changes, limits)
	request := func(t *testing.T, index, status int) {
		t.Helper()
		before := make([]uint64, len(watches))
		for i, watch := range watches {
			before[i], _ = watch.Snapshot()
		}
		w := httptest.NewRecorder()
		handle(w, httptest.NewRequest(http.MethodPost, "/sidebar/resync", nil), members[index])
		if w.Code != status {
			t.Fatalf("resync status = %d, want %d", w.Code, status)
		}
		if w.Body.Len() != 0 || (status == http.StatusTooManyRequests && w.Header().Get("Retry-After") != "1") {
			t.Fatalf("resync body=%q Retry-After=%q", w.Body.String(), w.Header().Get("Retry-After"))
		}
		for i, watch := range watches {
			want := before[i]
			if i == index && status == http.StatusNoContent {
				want++
			}
			if got, _ := watch.Snapshot(); got != want {
				t.Fatalf("watch %d generation = %d, want %d (status %d)", i, got, want, status)
			}
		}
	}
	t.Run("burst and refusal", func(t *testing.T) {
		for range 32 {
			request(t, 0, http.StatusNoContent)
		}
		for range 2 {
			request(t, 0, http.StatusTooManyRequests)
		}
	})
	for index, name := range []string{"other member", "other organisation"} {
		t.Run(name, func(t *testing.T) {
			for range 32 {
				request(t, index+1, http.StatusNoContent)
			}
			request(t, index+1, http.StatusTooManyRequests)
		})
	}
	t.Run("refill", func(t *testing.T) {
		now = now.Add(time.Second / 32)
		request(t, 0, http.StatusNoContent)
		request(t, 0, http.StatusTooManyRequests)
	})
}
