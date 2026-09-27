package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// TestOrgRoutesAgainstPostgreSQL runs the real session and authorisation
// stores: whoever may not see an organisation gets a 404 that carries none
// of its data, on every route in orgRoutes.
func TestOrgRoutesAgainstPostgreSQL(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	var acme, globex, alice, bob, carol domain.ID
	for _, q := range []struct {
		dest *domain.ID
		sql  string
	}{
		{&acme, "INSERT INTO organization (slug, name) VALUES ('acme', 'Acme Corporation') RETURNING id"},
		{&globex, "INSERT INTO organization (slug, name) VALUES ('globex', 'Globex Industries') RETURNING id"},
		{&alice, "INSERT INTO account (email, display_name, password_hash) VALUES ('alice@example.com', 'Alice', '$argon2id$x') RETURNING id"},
		{&bob, "INSERT INTO account (email, display_name, password_hash) VALUES ('bob@example.com', 'Bob', '$argon2id$x') RETURNING id"},
		{&carol, "INSERT INTO account (email, display_name, password_hash) VALUES ('carol@example.com', 'Carol', '$argon2id$x') RETURNING id"},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `WITH ids AS (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid)
		INSERT INTO member (organization_id, account_id, role, joined_event_seq)
		VALUES ($1, $3, 'owner', 1), ($2, $4, 'member', 1)`, acme, globex, alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO setup (organization_id) VALUES ($1)", acme); err != nil {
		t.Fatal(err)
	}

	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	clock := now
	sessions := auth.NewSessions(postgres.NewSessionStore(pool), func() time.Time { return clock })
	token := func(account domain.ID) string {
		t.Helper()
		value, _, err := sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	aliceToken, bobToken, carolToken, deletedToken := token(alice), token(bob), token(carol), token(alice)
	if err := sessions.Delete(ctx, deletedToken); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, Services{Sessions: sessions, Authz: authz.New(postgres.NewAuthzStore(pool))})
	if err != nil {
		t.Fatal(err)
	}
	get := func(method, path, cookie string, at time.Time) *httptest.ResponseRecorder {
		clock = at
		r := httptest.NewRequest(method, path, nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	routes := orgRoutes(&pageRenderer{})
	if len(routes) == 0 {
		t.Fatal("no organisation routes")
	}
	for _, route := range routes {
		path := "/o/acme" + strings.ReplaceAll(route.path, "{$}", "")
		for _, tt := range []struct {
			name   string
			cookie string
			at     time.Time
		}{
			{"signed out", "", now},
			{"account without a membership", carolToken, now},
			{"member of the other organisation", bobToken, now},
			{"deleted session", deletedToken, now},
			{"expired session", aliceToken, now.Add(auth.SessionLifetime)},
		} {
			t.Run(route.method+" "+path+" "+tt.name, func(t *testing.T) {
				w := get(route.method, path, tt.cookie, tt.at)
				body := w.Body.String()
				if w.Code != http.StatusNotFound || strings.Contains(body, "Acme") || strings.Contains(body, "Globex") {
					t.Fatalf("status %d, body %q", w.Code, body)
				}
			})
		}
		t.Run(route.method+" "+path+" member", func(t *testing.T) {
			w := get(route.method, path, aliceToken, now)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Acme Corporation") {
				t.Fatalf("status %d, body %q", w.Code, w.Body.String())
			}
		})
	}

	// "/" sends a member of the setup organisation there, and only them.
	if w := get(http.MethodGet, "/", aliceToken, now); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/o/acme/" {
		t.Fatalf("alice at /: %d %q", w.Code, w.Header().Get("Location"))
	}
	for name, cookie := range map[string]string{"member of the other organisation only": bobToken, "signed out": ""} {
		if w := get(http.MethodGet, "/", cookie, now); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Acme") {
			t.Fatalf("%s at /: %d", name, w.Code)
		}
	}
	if logs.Len() != 0 {
		t.Errorf("unexpected message fallback: %s", logs.String())
	}
}

// oneOrganisation makes the "live" session's account the owner of acme.
type oneOrganisation struct{}

func (oneOrganisation) Member(_ context.Context, account *domain.Account, slug string) (authz.Membership, error) {
	if account == nil || slug != "acme" {
		return authz.Membership{}, authz.ErrNotFound
	}
	return authz.Membership{Organization: domain.Organization{Slug: "acme", Name: "Acme Corporation"}, Member: domain.Member{Role: domain.RoleOwner}}, nil
}

func (oneOrganisation) HomeSlug(context.Context, *domain.Account) (string, error) {
	return "acme", nil
}

func TestOrgHomeRendering(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, Services{Sessions: oneSession{}, Authz: oneOrganisation{}})
	if err != nil {
		t.Fatal(err)
	}
	for lang, texts := range map[string][]string{
		"en": {"Acme Corporation", "Signed in as", "Alice", "Owner", "Sign out"},
		"ja": {"Acme Corporation", "サインイン中:", "Alice", "オーナー", "サインアウト"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/o/acme/", nil)
		r.Header.Set("Accept-Language", lang)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<form method="post" action="/signout">`) {
			t.Fatalf("%s: status %d, body %s", lang, w.Code, body)
		}
		for _, text := range texts {
			if !strings.Contains(body, text) {
				t.Errorf("%s page lacks %q", lang, text)
			}
		}
	}
}
