package web

import (
	"bytes"
	"context"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestOrgRoutesAgainstPostgreSQL runs the real session and authorisation
// stores: whoever may not see an organisation gets a 404 that carries none
// of its data, on every route in orgRoutes.
func TestOrgRoutesAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL on purpose: setup and sign-up create one organisation and join
	// everyone to it, so they cannot build a second organisation (globex, with
	// Bob) or an account with no membership (Carol, until she joins later).
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
		INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
		VALUES ($1, $3, 'owner', 1, 'alice'), ($2, $4, 'member', 1, 'bob')`, acme, globex, alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO setup (organization_id) VALUES ($1)", acme); err != nil {
		t.Fatal(err)
	}

	var acmeChannel, globexChannel domain.ID
	for org, dest := range map[domain.ID]*domain.ID{acme: &acmeChannel, globex: &globexChannel} {
		if err := pool.QueryRow(ctx, "INSERT INTO channel (organization_id, name, is_default) VALUES ($1, '雑談', true) RETURNING id", org).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	// Subtests stay sequential: they share this clock and add Carol's membership later.
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
	services := postgresServices(t, pool, sessions, "", false)
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	get := func(method, path, cookie string, at time.Time) *httptest.ResponseRecorder {
		clock = at
		return serveForm(handler, method, path, cookie, url.Values{"name": {"新しいチャンネル"}, "body": {"posted through the page"}})
	}

	routes := orgRoutes(&pageRenderer{}, services.Channels, services.Messages, services.Posting, services.Stream)
	if len(routes) == 0 {
		t.Fatal("no organisation routes")
	}
	for _, route := range routes {
		path := "/organizations/acme" + strings.ReplaceAll(route.path, "{$}", "")
		if strings.Contains(path, "{channelID}") {
			// Keep what follows the channel, such as /events.
			path = view.ChannelURL("acme", acmeChannel) + strings.TrimPrefix(route.path, "/channels/{channelID}")
		}
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
			switch route.method + " " + route.path {
			case "GET /{$}":
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != view.ChannelURL("acme", acmeChannel) {
					t.Fatalf("default: %d %s", w.Code, w.Header().Get("Location"))
				}
			case "GET /channels/{channelID}":
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Acme Corporation") || !strings.Contains(w.Body.String(), "雑談") {
					t.Fatalf("channel: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels/{channelID}":
				if w.Code != 303 || w.Header().Get("Location") != view.ChannelURL("acme", acmeChannel) {
					t.Fatalf("post: %d %s", w.Code, w.Body.String())
				}
			case "GET /channels/{channelID}/events":
				// This suite runs without Services.Stream, so a member gets the
				// stream-off 404 after authorisation; the stream itself is
				// tested through the production wiring in cmd/ribbitto.
				if w.Code != http.StatusNotFound {
					t.Fatalf("events: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels":
				created, err := services.Channels.List(ctx, authz.Membership{Organization: domain.Organization{ID: acme}})
				if err != nil {
					t.Fatal(err)
				}
				var location string
				for _, c := range created {
					if c.Name == "新しいチャンネル" {
						location = view.ChannelURL("acme", c.ID)
					}
				}
				if location == "" || w.Code != http.StatusSeeOther || w.Header().Get("Location") != location {
					t.Fatalf("create: %d %s", w.Code, w.Header().Get("Location"))
				}
			default:
				t.Fatal("route lacks a success expectation")
			}
		})
	}

	t.Run("member cannot read another organisation's channel under own URL", func(t *testing.T) {
		w := get(http.MethodGet, view.ChannelURL("acme", globexChannel), aliceToken, now)
		if w.Code != http.StatusNotFound || w.Body.String() != "404 page not found\n" {
			t.Fatalf("status %d, body %q", w.Code, w.Body.String())
		}
	})
	for _, route := range routes {
		path := "/organizations/missing" + strings.ReplaceAll(route.path, "{$}", "")
		path = strings.ReplaceAll(path, "{channelID}", strings.TrimPrefix(view.ChannelURL("acme", acmeChannel), "/organizations/acme/channels/"))
		if w := get(route.method, path, aliceToken, now); w.Code != http.StatusNotFound {
			t.Fatalf("unknown organisation: %d", w.Code)
		}
	}

	t.Run("legacy path is unknown even for a member", func(t *testing.T) {
		w := get(http.MethodGet, "/o/acme/", aliceToken, now)
		if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" || w.Body.String() != "404 page not found\n" {
			t.Fatalf("status %d, location %q, body %q", w.Code, w.Header().Get("Location"), w.Body.String())
		}
	})

	// "/" sends a member of the setup organisation there, and only them.
	if w := get(http.MethodGet, "/", aliceToken, now); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/organizations/acme/" {
		t.Fatalf("alice at /: %d %q", w.Code, w.Header().Get("Location"))
	}
	for name, cookie := range map[string]string{"member of the other organisation only": bobToken, "signed out": ""} {
		if w := get(http.MethodGet, "/", cookie, now); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Acme") {
			t.Fatalf("%s at /: %d", name, w.Code)
		}
	}
	t.Run("author lookup isolation and reload", func(t *testing.T) {
		authzService := authz.New(postgres.NewAuthzStore(pool))
		a, err := authzService.Member(ctx, &domain.Account{ID: alice}, "acme")
		if err != nil {
			t.Fatal(err)
		}
		b, err := authzService.Member(ctx, &domain.Account{ID: bob}, "globex")
		if err != nil {
			t.Fatal(err)
		}
		// Give Carol a membership only after the non-member assertions above.
		if _, err := pool.Exec(ctx, "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, 'member', 2, 'carol')", acme, carol); err != nil {
			t.Fatal(err)
		}
		members, err := postgres.NewMemberStore(pool).LookupMembers(ctx, acme, []domain.ID{a.Member.ID, b.Member.ID})
		if err != nil || len(members) != 1 || members[a.Member.ID].AccountID != alice || members[a.Member.ID].Handle != "alice" {
			t.Fatalf("members: %v, %v", members, err)
		}
		ids := []domain.ID{}
		for _, m := range members {
			ids = append(ids, m.AccountID)
		}
		names, err := postgres.NewAccountStore(pool).LookupDisplayNames(ctx, ids)
		if err != nil || len(names) != 1 || names[alice] != "Alice" {
			t.Fatalf("names: %v, %v", names, err)
		}
		posted := get("POST", view.ChannelURL("acme", acmeChannel), aliceToken, now)
		reloaded := get("GET", view.ChannelURL("acme", acmeChannel), carolToken, now)
		if posted.Code != 303 || reloaded.Code != 200 || !strings.Contains(reloaded.Body.String(), "posted through the page") || !strings.Contains(reloaded.Body.String(), "@alice") {
			t.Fatalf("page post/reload: %d / %d", posted.Code, reloaded.Code)
		}
		for _, path := range []string{view.ChannelURL("acme", acmeChannel), view.ChannelURL("globex", acmeChannel)} {
			if w := get("POST", path, bobToken, now); w.Code != 404 || w.Body.String() != "404 page not found\n" {
				t.Fatalf("foreign post: %d %s", w.Code, w.Body.String())
			}
		}
		poster := message.New(postgres.NewPostingStore(pool))
		for range 51 {
			if _, err := poster.Post(ctx, a, acmeChannel, "hello after reload"); err != nil {
				t.Fatal(err)
			}
		}
		w := get("GET", view.ChannelURL("acme", acmeChannel), carolToken, now)
		if w.Code != 200 || strings.Count(w.Body.String(), ">hello after reload</p>") != 50 || !strings.Contains(w.Body.String(), "@alice") {
			t.Fatalf("reload: %d %s", w.Code, w.Body.String())
		}
		w = get("GET", view.ChannelURL("acme", acmeChannel), bobToken, now)
		if w.Code != 404 || strings.Contains(w.Body.String(), "hello after reload") {
			t.Fatal("foreign member saw history")
		}
	})

	if logs.Len() != 0 {
		t.Errorf("unexpected message fallback: %s", logs.String())
	}
}

func serveForm(handler http.Handler, method, path, cookie string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// oneOrganisation makes the "live" session's account the owner of acme.
type oneOrganisation struct{}

func (oneOrganisation) Member(_ context.Context, account *domain.Account, slug string) (authz.Membership, error) {
	if account == nil || slug != "acme" {
		return authz.Membership{}, authz.ErrNotFound
	}
	return authz.Membership{Organization: domain.Organization{Slug: "acme", Name: "Acme Corporation"}, Member: domain.Member{Role: domain.RoleOwner, Handle: "alice"}}, nil
}

func (oneOrganisation) HomeSlug(context.Context, *domain.Account) (string, error) {
	return "acme", nil
}

func TestHomeSignUpLink(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, open := range []bool{true, false} {
		handler, err := NewHandler("", catalogues, testServices(func(s *Services) {
			s.SignUp, s.SetupSessions = fakeSignUp{&fakeSetup{open: open}}, &fakeSetup{}
		}))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `href="/signin"`) || strings.Contains(body, `href="/signup"`) != open {
			t.Fatalf("sign-up open %v: status %d, body %s", open, w.Code, body)
		}
	}
}

func TestChannelRendering(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice))
	if err != nil {
		t.Fatal(err)
	}
	for lang, texts := range map[string][]string{
		// The spaces around the member's name are part of the checked text.
		"en": {"Acme Corporation", `Signed in as <bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span> · Owner`, "Sign out"},
		"ja": {"Acme Corporation", `サインイン中: <bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span> · オーナー`, "サインアウト"},
	} {
		r := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", domain.ID{1}), nil)
		r.Header.Set("Accept-Language", lang)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		// The signed-in organisation page gets the same security headers
		// and script nonces as every other page (TestHTMLSecurity).
		nonce := responseNonce(t, w)
		if scripts := strings.Count(body, "<script "); scripts < 5 || strings.Count(body, ` nonce="`+nonce+`"`) != scripts {
			t.Errorf("%s: scripts lack the response nonce", lang)
		}
		if w.Code != http.StatusOK || !strings.Contains(body, `<form method="post" action="/signout">`) {
			t.Fatalf("%s: status %d, body %s", lang, w.Code, body)
		}
		if !strings.Contains(body, "雑談 &lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(body, "<script>alert(1)</script>") {
			t.Fatal("channel name was not escaped")
		}
		doc, err := html.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		selected := 0
		for n := range doc.Descendants() {
			if attr(n, "aria-current") == "page" {
				selected++
				if n.DataAtom != atom.A || attr(n, "href") != view.ChannelURL("acme", domain.ID{1}) {
					t.Fatal("wrong current channel link")
				}
				for _, class := range []string{"text-on-selected", "border-l-3", "border-brand"} {
					if !slices.Contains(strings.Fields(attr(n, "class")), class) {
						t.Errorf("selected link lacks %s", class)
					}
				}
			}
		}
		if selected != 1 {
			t.Fatalf("%d selected channels", selected)
		}

		for _, text := range texts {
			if !strings.Contains(body, text) {
				t.Errorf("%s page lacks %q", lang, text)
			}
		}
	}
}
