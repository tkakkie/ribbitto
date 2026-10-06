package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

const acceptanceToken = "acceptance-setup-token-at-least-32-characters"

func acceptanceForm(name string) url.Values {
	return url.Values{"token": {acceptanceToken}, "organization_name": {"Private " + name},
		"slug": {name}, "display_name": {name}, "handle": {name}, "email": {name + "@example.com"},
		"password": {"acceptance password for " + name}}
}

func acceptanceDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.NewEmpty(t)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { acceptanceOK(t, db.Close()) }()
	acceptanceOK(t, platform.Migrate(t.Context(), db, "up", io.Discard))
	return pool
}

func acceptanceServer(t *testing.T, pool *pgxpool.Pool, signup string) *httptest.Server {
	t.Helper()
	enabled, err := signupEnabled(signup)
	acceptanceOK(t, err)
	handler, _, err := buildHandler(t.Context(), pool, handlerConfig{setupToken: acceptanceToken,
		signupEnabled: enabled, trustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	acceptanceOK(t, err)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

type acceptanceBrowser struct {
	server *httptest.Server
	client *http.Client
	ip     string
}

func newAcceptanceBrowser(t *testing.T, server *httptest.Server, ip string) acceptanceBrowser {
	t.Helper()
	client := *server.Client()
	var err error
	client.Jar, err = cookiejar.New(nil)
	acceptanceOK(t, err)
	client.Timeout = 15 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return acceptanceBrowser{server, &client, ip}
}

func (b acceptanceBrowser) request(t *testing.T, method, path string, form url.Values) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, b.server.URL+path, strings.NewReader(form.Encode()))
	acceptanceOK(t, err)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", b.server.URL)
	r.Header.Set("X-Forwarded-For", b.ip)
	return r
}

func (b acceptanceBrowser) visit(t *testing.T, method, path string, form url.Values, status int) (*http.Response, string) {
	t.Helper()
	response, err := b.client.Do(b.request(t, method, path, form))
	acceptanceOK(t, err)
	return response, acceptanceResponse(t, response, status)
}

func acceptanceResponse(t *testing.T, response *http.Response, status int) string {
	t.Helper()
	defer func() { acceptanceOK(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	acceptanceOK(t, err)
	if response.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d; body: %s", response.Request.Method, response.Request.URL.Path, response.StatusCode, status, body)
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") && !strings.Contains(response.Header.Get("Content-Security-Policy"), "script-src 'nonce-") {
		t.Fatal("HTML response missing nonce CSP")
	}
	if status == http.StatusSeeOther {
		want := "/"
		if response.Request.URL.Path == "/signout" {
			want = "/signin"
		} else if response.Request.URL.Path == "/" {
			want = "/organizations/owner/"
		} else if path := response.Request.URL.Path; strings.HasPrefix(path, "/organizations/") && strings.HasSuffix(path, "/") {
			want = response.Header.Get("Location")
			matched, err := regexp.MatchString("^"+regexp.QuoteMeta(path)+`channels/[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, want)
			acceptanceOK(t, err)
			if !matched {
				t.Fatalf("organisation redirect is not a scoped channel UUID: %q", want)
			}
		}
		if response.Header.Get("Location") != want {
			t.Fatalf("redirect = %q, want %q", response.Header.Get("Location"), want)
		}
	}
	return string(body)
}

func acceptanceOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func acceptanceCount(t *testing.T, pool *pgxpool.Pool, want int, query string, args ...any) {
	t.Helper()
	var count int
	acceptanceOK(t, pool.QueryRow(t.Context(), query, args...).Scan(&count))
	if count != want {
		t.Fatalf("%s: count %d, want %d", query, count, want)
	}
}

func acceptanceCookie(t *testing.T, pool *pgxpool.Pool, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name != middleware.SessionCookie {
			continue
		}
		if !strings.HasPrefix(cookie.Name, "__Host-") || cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
			t.Fatalf("invalid session cookie attributes: %+v", cookie)
		}
		raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
		acceptanceOK(t, err)
		hash := sha256.Sum256(raw)
		acceptanceCount(t, pool, 1, `SELECT count(*) FROM session WHERE token_hash = $1 AND token_hash <> $2 AND strpos(row_to_json(session)::text, $3) = 0`, hash[:], raw, cookie.Value)
		return cookie
	}
	t.Fatal("missing session cookie")
	return nil
}

func TestAccountsAcceptance(t *testing.T) {
	pool := acceptanceDatabase(t)
	server := acceptanceServer(t, pool, "on")
	owner := newAcceptanceBrowser(t, server, "192.0.2.1")
	owner.visit(t, "GET", "/", nil, 200)
	owner.visit(t, "GET", "/setup", nil, 200)
	wrong := acceptanceForm("owner")
	wrong.Set("token", "wrong")
	owner.visit(t, "POST", "/setup", wrong, 422)
	for _, table := range []string{"organization", "account", "member", "setup", "session"} {
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM "+table)
	}
	limited := newAcceptanceBrowser(t, server, "192.0.2.2")
	for i := 0; i < 4; i++ {
		status := 422
		if i == 3 {
			status = 429
		}
		limited.visit(t, "POST", "/setup", wrong, status)
	}
	response, _ := owner.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	cookie := acceptanceCookie(t, pool, response)
	acceptanceCount(t, pool, 1, `SELECT count(*) FROM setup s JOIN organization o ON o.id = s.organization_id JOIN member m ON m.organization_id = o.id JOIN account a ON a.id = m.account_id WHERE o.slug = 'owner' AND o.name = 'Private owner' AND a.email = 'owner@example.com' AND a.display_name = 'owner' AND m.role = 'owner' AND m.handle = 'owner'`)
	owner.visit(t, "GET", "/", nil, 303)
	response, _ = owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	_, body := owner.visit(t, "GET", response.Header.Get("Location"), nil, 200)
	if !strings.Contains(body, "Private owner") || !strings.Contains(body, "@owner") {
		t.Fatal("owner's organisation or handle not rendered")
	}
	owner.visit(t, "GET", "/setup", nil, 404)
	owner.visit(t, "POST", "/setup", acceptanceForm("late"), 404)
	owner.visit(t, "POST", "/signout", nil, 303)
	acceptanceCount(t, pool, 0, "SELECT count(*) FROM session")
	owner.visit(t, "GET", "/organizations/owner/", nil, 404)
	// Replay the deleted token, rather than merely relying on an empty jar.
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	owner.client.Jar.SetCookies(u, []*http.Cookie{cookie})
	owner.visit(t, "GET", "/organizations/owner/", nil, 404)
	owner.visit(t, "GET", "/signin", nil, 200)
	response, _ = owner.visit(t, "POST", "/signin", acceptanceForm("owner"), 303)
	acceptanceCookie(t, pool, response)
	owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	cross := owner.request(t, "POST", "/signout", nil)
	cross.Header.Set("Origin", "https://attacker.example")
	response, err = owner.client.Do(cross)
	acceptanceOK(t, err)
	acceptanceResponse(t, response, 403)
	owner.visit(t, "GET", "/organizations/owner/", nil, 303)
	_, err = pool.Exec(t.Context(), "UPDATE session SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day'")
	acceptanceOK(t, err)
	owner.visit(t, "GET", "/organizations/owner/", nil, 404)

	member := newAcceptanceBrowser(t, server, "192.0.2.3")
	member.visit(t, "GET", "/signup", nil, 200)
	response, _ = member.visit(t, "POST", "/signup", acceptanceForm("member"), 303)
	acceptanceCookie(t, pool, response)
	acceptanceCount(t, pool, 1, `SELECT count(*) FROM account a JOIN member m ON m.account_id = a.id JOIN setup s ON s.organization_id = m.organization_id WHERE a.email = 'member@example.com' AND m.role = 'member'`)
	member.visit(t, "GET", "/organizations/owner/", nil, 303)
	off := newAcceptanceBrowser(t, acceptanceServer(t, pool, "off"), "192.0.2.4")
	off.visit(t, "GET", "/signup", nil, 404)
	off.visit(t, "POST", "/signup", acceptanceForm("refused"), 404)
	acceptanceCount(t, pool, 2, "SELECT count(*) FROM account")
	acceptanceCount(t, pool, 2, "SELECT count(*) FROM member")
	for _, limit := range []struct {
		path  string
		burst int
	}{{"/signin", 5}, {"/signup", 3}} {
		form := acceptanceForm("member")
		form.Set("password", "a different valid password")
		for i := 0; i <= limit.burst; i++ {
			status := 422
			if i == limit.burst {
				status = 429
			}
			limited.visit(t, "POST", limit.path, form, status)
		}
	}

	_, err = pool.Exec(t.Context(), "INSERT INTO organization (slug, name) VALUES ('other', 'Hidden second organisation')")
	acceptanceOK(t, err)
	// A channel needs its default topic in the same statement (decision 21).
	_, err = pool.Exec(t.Context(), `WITH c AS (INSERT INTO channel (organization_id, name, is_default) SELECT id, 'general', true FROM organization WHERE slug = 'other' RETURNING *)
		INSERT INTO topic (id, organization_id, channel_id, is_default) SELECT default_topic_id, organization_id, id, true FROM c`)
	acceptanceOK(t, err)
	// Move the registered member so both directions of isolation are exercised.
	_, err = pool.Exec(t.Context(), `UPDATE member SET organization_id = (SELECT id FROM organization WHERE slug = 'other') WHERE account_id = (SELECT id FROM account WHERE email = 'member@example.com')`)
	acceptanceOK(t, err)
	response, _ = member.visit(t, "GET", "/organizations/other/", nil, 303)
	_, body = member.visit(t, "GET", response.Header.Get("Location"), nil, 200)
	if !strings.Contains(body, "Hidden second organisation") || strings.Contains(body, "Private owner") {
		t.Fatal("second organisation page has wrong data")
	}
	owner.visit(t, "POST", "/signin", acceptanceForm("owner"), 303)
	for _, browser := range []acceptanceBrowser{limited, member, owner} {
		for _, slug := range []string{"owner", "other"} {
			if (browser.ip == owner.ip && slug == "owner") || (browser.ip == member.ip && slug == "other") {
				continue
			}
			_, body = browser.visit(t, "GET", "/organizations/"+slug+"/", nil, 404)
			if strings.Contains(body, "Private owner") || strings.Contains(body, "Hidden second organisation") || strings.Contains(body, "@example.com") {
				t.Fatal("denied response leaked organisation data")
			}
		}
	}
}

func TestSetupAcceptanceRace(t *testing.T) {
	pool := acceptanceDatabase(t)
	server := acceptanceServer(t, pool, "on")
	type result struct {
		index    int
		response *http.Response
		err      error
	}
	results := make(chan result, 10)
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		browser := newAcceptanceBrowser(t, server, fmt.Sprintf("192.0.2.%d", i+10))
		request := browser.request(t, "POST", "/setup", acceptanceForm(fmt.Sprintf("racer-%d", i)))
		go func() {
			<-start
			response, err := browser.client.Do(request)
			results <- result{i, response, err}
		}()
	}
	close(start)
	winners := 0
	for range 10 {
		r := <-results
		acceptanceOK(t, r.err)
		status := 404
		if r.response.StatusCode == 303 {
			status = 303
			winners++
			acceptanceCookie(t, pool, r.response)
			acceptanceCount(t, pool, 1, `SELECT count(*) FROM setup s JOIN organization o ON o.id = s.organization_id JOIN member m ON m.organization_id = o.id JOIN account a ON a.id = m.account_id WHERE o.slug = $1 AND a.email = $2 AND m.role = 'owner'`, fmt.Sprintf("racer-%d", r.index), fmt.Sprintf("racer-%d@example.com", r.index))
		}
		acceptanceResponse(t, r.response, status)
	}
	if winners != 1 {
		t.Fatalf("setup winners = %d, want 1", winners)
	}
	for _, table := range []string{"organization", "account", "member", "setup", "session"} {
		acceptanceCount(t, pool, 1, "SELECT count(*) FROM "+table)
	}
}

// acceptanceSessionCount counts the session rows for a cookie's token.
func acceptanceSessionCount(t *testing.T, pool *pgxpool.Pool, want int, cookie *http.Cookie) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	acceptanceOK(t, err)
	hash := sha256.Sum256(raw)
	acceptanceCount(t, pool, want, "SELECT count(*) FROM session WHERE token_hash = $1", hash[:])
}

// Every flow that issues a session replaces the browser's previous one.
func TestSessionReplacedAcceptance(t *testing.T) {
	pool := acceptanceDatabase(t)
	server := acceptanceServer(t, pool, "on")
	u, err := url.Parse(server.URL)
	acceptanceOK(t, err)
	browser := newAcceptanceBrowser(t, server, "192.0.2.10")

	// Setup with a stale but well-formed cookie still signs the owner in.
	stale := &http.Cookie{Name: middleware.SessionCookie, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Path: "/"}
	browser.client.Jar.SetCookies(u, []*http.Cookie{stale})
	response, _ := browser.visit(t, "POST", "/setup", acceptanceForm("owner"), 303)
	t1 := acceptanceCookie(t, pool, response)
	browser.visit(t, "GET", "/organizations/owner/", nil, 303)

	// A failed sign-up in the signed-in browser keeps T1.
	browser.visit(t, "POST", "/signup", acceptanceForm("owner"), 422)
	acceptanceSessionCount(t, pool, 1, t1)
	browser.visit(t, "GET", "/organizations/owner/", nil, 303)

	// Signing up as B in the same browser ends T1; T2 belongs to B.
	response, _ = browser.visit(t, "POST", "/signup", acceptanceForm("member"), 303)
	t2 := acceptanceCookie(t, pool, response)
	acceptanceSessionCount(t, pool, 0, t1)
	raw, err := base64.RawURLEncoding.DecodeString(t2.Value)
	acceptanceOK(t, err)
	hash := sha256.Sum256(raw)
	acceptanceCount(t, pool, 1, `SELECT count(*) FROM session s JOIN account a ON a.id = s.account_id WHERE s.token_hash = $1 AND a.email = 'member@example.com'`, hash[:])
	acceptanceCount(t, pool, 1, "SELECT count(*) FROM session")

	// Replaying T1 finds no session.
	replay := newAcceptanceBrowser(t, server, "192.0.2.11")
	replay.client.Jar.SetCookies(u, []*http.Cookie{t1})
	replay.visit(t, "GET", "/organizations/owner/", nil, 404)
}
