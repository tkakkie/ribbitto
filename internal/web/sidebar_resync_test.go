package web

import (
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
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
