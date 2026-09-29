package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/web/static"
)

func TestHandler(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		code int
	}{
		{"/", http.StatusOK},
		{"/healthz", http.StatusOK},
		{"/static/css/app.css", http.StatusOK},
		{"/static/vendor/htmx-2.0.7.min.js", http.StatusOK},
		{"/static/vendor/htmx-ext-sse-2.2.4.min.js", http.StatusOK},
		{"/static/vendor/idiomorph-ext-0.7.4.min.js", http.StatusOK},
		{"/static/missing.js", http.StatusNotFound},
		{"/static/css/", http.StatusNotFound},
		{"/missing", http.StatusNotFound},
		{"/nested/path", http.StatusNotFound},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != tt.code {
				t.Fatalf("status = %d, want %d", w.Code, tt.code)
			}
			if strings.HasPrefix(tt.path, "/static/") && tt.code == http.StatusOK {
				if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
					t.Errorf("Cache-Control = %q", got)
				}
				want, err := fs.ReadFile(static.FS(), strings.TrimPrefix(tt.path, "/static/"))
				if err != nil {
					t.Fatal(err)
				}
				if w.Body.String() != string(want) {
					t.Error("response differs from embedded asset")
				}
			}
		})
	}
}

// TestStaticRanges: a single range is served as usual, but a request for
// several ranges gets the whole asset, so many tiny ranges cannot multiply
// the response with a multipart header per part.
func TestStaticRanges(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	const path = "/static/vendor/htmx-2.0.7.min.js"
	asset, err := fs.ReadFile(static.FS(), strings.TrimPrefix(path, "/static/"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, rangeHeader string
		code              int
		body              []byte
	}{
		{"single range", "bytes=0-9", http.StatusPartialContent, asset[:10]},
		{"several ranges", "bytes=0-0,2-2,4-4", http.StatusOK, asset},
		{"several ranges with spaces", "bytes=0-0, 2-2", http.StatusOK, asset},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Range", tt.rangeHeader)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.code {
				t.Fatalf("status = %d, want %d", w.Code, tt.code)
			}
			if !bytes.Equal(w.Body.Bytes(), tt.body) {
				t.Fatalf("body is %d bytes, want %d", w.Body.Len(), len(tt.body))
			}
		})
	}
}

func TestHello(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	css, err := fs.ReadFile(static.FS(), "css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	stylesheetURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256(css))
	body := w.Body.String()
	for _, want := range []string{
		`<html lang="en">`, "Hello from ribbitto",
		`<link rel="stylesheet" href="` + stylesheetURL + `">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	previous := -1
	nonce := responseNonce(t, w)
	for _, file := range []string{"htmx-2.0.7.min.js", "htmx-ext-sse-2.2.4.min.js", "idiomorph-ext-0.7.4.min.js"} {
		index := strings.Index(body, `<script defer src="/static/vendor/`+file+`" nonce="`+nonce+`"></script>`)
		if index <= previous {
			t.Errorf("script %s missing or out of order", file)
		}
		previous = index
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, stylesheetURL, nil))
	if w.Code != http.StatusOK || w.Body.String() != string(css) {
		t.Error("hashed stylesheet URL does not serve the embedded CSS")
	}
}

func TestHTMLSecurity(t *testing.T) {
	scripts := regexp.MustCompile(`<script\b[^>]*>`)
	config := regexp.MustCompile(`<meta name="htmx-config" content='([^']*)'`)
	// Keep this table complete as HTML pages are added.
	for _, tt := range []struct{ name, path, lang, assets string }{
		{"embedded English", "/", "en", ""},
		{"embedded Japanese", "/", "ja", ""},
		{"development English", "/", "en", "../../web/static"},
		{"development Japanese", "/", "ja", "../../web/static"},
		{"signup English", "/signup", "en", ""},
		{"signup Japanese", "/signup", "ja", ""},
		{"setup English", "/setup", "en", ""},
		{"setup Japanese", "/setup", "ja", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := newTestHandler(t, tt.assets)
			if err != nil {
				t.Fatal(err)
			}
			previous := ""
			for range 2 {
				r := httptest.NewRequest(http.MethodGet, tt.path, nil)
				r.Header.Set("Accept-Language", tt.lang)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
					t.Fatalf("expected HTML page, got status %d and headers %v", w.Code, w.Header())
				}
				nonce := responseNonce(t, w)
				if nonce == previous {
					t.Error("two responses reused a nonce")
				}
				previous = nonce
				tags := scripts.FindAllString(w.Body.String(), -1)
				if len(tags) != 3 {
					t.Fatalf("got %d scripts, want 3", len(tags))
				}
				for _, tag := range tags {
					if !strings.Contains(tag, ` nonce="`+nonce+`"`) {
						t.Errorf("script lacks response nonce: %s", tag)
					}
				}
				match := config.FindStringSubmatch(w.Body.String())
				if len(match) != 2 {
					t.Fatal("missing htmx-config meta tag")
				}
				var settings map[string]bool
				if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &settings); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"allowEval", "allowScriptTags", "includeIndicatorStyles"} {
					if value, exists := settings[name]; !exists || value {
						t.Errorf("htmx %s must explicitly be false", name)
					}
				}
				if strings.Index(w.Body.String(), match[0]) > strings.Index(w.Body.String(), tags[0]) {
					t.Error("htmx configuration must precede scripts")
				}
			}
		})
	}
}

func responseNonce(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	policy := w.Header().Get("Content-Security-Policy")
	_, rest, found := strings.Cut(policy, "script-src 'nonce-")
	nonce, _, closed := strings.Cut(rest, "'")
	if !found || !closed {
		t.Fatalf("missing script nonce in CSP: %q", policy)
	}
	decoded, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("nonce must encode 32 random bytes: %q (%v)", nonce, err)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'nonce-" + nonce + "'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	return nonce
}

func TestCrossOriginProtection(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"foreign origin without Sec-Fetch-Site", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		// No POST route exists yet, so a request that passes the protection
		// reaches the router and gets 405 for GET-only "/".
		{"same origin", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, http.StatusMethodNotAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://example.com/", strings.NewReader("a=b"))
			for name, value := range tt.headers {
				r.Header.Set(name, value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
		})
	}
}

func TestDevelopmentAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	cssPath := filepath.Join(dir, "css", "app.css")
	initialCSS := "body { color: red; }"
	if err := os.WriteFile(cssPath, []byte(initialCSS), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	previousURL := ""
	for _, css := range []string{initialCSS, "body { color: blue; }"} {
		if err := os.WriteFile(cssPath, []byte(css), 0o600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		wantURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256([]byte(css)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<link rel="stylesheet" href="`+wantURL+`">`) {
			t.Fatalf("page does not link to current CSS: status = %d, body = %s", w.Code, w.Body.String())
		}
		if previousURL != "" && strings.Contains(w.Body.String(), previousURL) {
			t.Error("page still contains the previous stylesheet URL")
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wantURL, nil))
		if w.Code != http.StatusOK || w.Body.String() != css {
			t.Fatalf("stylesheet does not serve current CSS: status = %d, body = %q", w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		previousURL = wantURL
	}
}

func newTestHandler(t *testing.T, dir string) (http.Handler, error) {
	t.Helper()
	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if logs.Len() != 0 {
			t.Errorf("unexpected message fallback: %s", logs.String())
		}
	})
	setup := &fakeSetup{open: true}
	return NewHandler(dir, catalogues, Services{Posting: testPoster(), Messages: fakeMessages{}, Channels: &fakeChannels{}, Authz: noOrganisations{}, Sessions: noSessions{}, SignIn: &fakeSignIn{}, Setup: setup, SetupSessions: setup, SignUp: fakeSignUp{&fakeSetup{open: true}}})
}

func TestHelloLanguages(t *testing.T) {
	handler, err := newTestHandler(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, cookie, header, lang string }{
		{"cookie English", "en", "ja", "en"},
		{"cookie Japanese", "ja", "en", "ja"},
		{"invalid cookie", "fr", "ja", "ja"},
		{"regional cookie is invalid", "ja-JP", "en", "en"},
		{"regional header", "", "ja-JP", "ja"},
		{"weighted Japanese", "", "en;q=0.2, ja;q=0.9", "ja"},
		{"weighted English", "", "ja;q=0.2, en;q=0.9", "en"},
		{"zero weight", "", "ja;q=0, en;q=0.5", "en"},
		{"absent", "", "", "en"},
		{"malformed", "", "ja;q=broken", "en"},
		{"unsupported", "", "fr", "en"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Language", tt.header)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "lang", Value: tt.cookie})
			}
			w := httptest.NewRecorder()
			w.Header().Add("Vary", "Accept-Encoding")
			if tt.cookie != "" {
				w.Header().Add("Vary", "cookie")
			}
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			text, title := "Hello from ribbitto", "<title>Home · ribbitto</title>"
			if tt.lang == "ja" {
				text, title = "ribbittoからこんにちは", "<title>ホーム · ribbitto</title>"
			}
			for _, want := range []string{`<html lang="` + tt.lang + `">`, title, text} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("HTML missing %q", want)
				}
			}
			tokens := map[string]int{}
			for _, value := range w.Header().Values("Vary") {
				for _, token := range strings.Split(value, ",") {
					tokens[strings.ToLower(strings.TrimSpace(token))]++
				}
			}
			for _, token := range []string{"accept-encoding", "accept-language", "cookie"} {
				if tokens[token] != 1 {
					t.Errorf("Vary tokens = %v; want %s once", tokens, token)
				}
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Error("language negotiation must not set a cookie")
			}
		})
	}
}

// noOrganisations makes nobody a member of anything.
type noOrganisations struct{}

func (noOrganisations) Member(context.Context, *domain.Account, string) (authz.Membership, error) {
	return authz.Membership{}, authz.ErrNotFound
}

func (noOrganisations) HomeSlug(context.Context, *domain.Account) (string, error) {
	return "", authz.ErrNotFound
}

// noSessions signs nobody in, for tests that do not need a session.
type noSessions struct{}

func (noSessions) Resolve(context.Context, string) (domain.Account, error) {
	return domain.Account{}, auth.ErrNoSession
}

// countingResolver counts lookups and fails every one, like a database
// outage.
type countingResolver struct{ calls int }

func (c *countingResolver) Resolve(context.Context, string) (domain.Account, error) {
	c.calls++
	return domain.Account{}, errors.New("connection refused")
}

func TestNewHandlerRequiresServices(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for name, services := range map[string]Services{
		"no channels":                       {Sessions: noSessions{}, SignIn: &fakeSignIn{}, Authz: noOrganisations{}, Posting: testPoster(), Messages: fakeMessages{}},
		"no messages":                       {Sessions: noSessions{}, SignIn: &fakeSignIn{}, Authz: noOrganisations{}, Channels: &fakeChannels{}, Posting: testPoster()},
		"no posting":                        {Sessions: noSessions{}, SignIn: &fakeSignIn{}, Authz: noOrganisations{}, Channels: &fakeChannels{}, Messages: fakeMessages{}},
		"no sessions":                       {SignIn: &fakeSignIn{}},
		"no sign-in":                        {Sessions: noSessions{}},
		"setup without a session creator":   {Sessions: noSessions{}, SignIn: &fakeSignIn{}, Posting: testPoster(), Messages: fakeMessages{}, Channels: &fakeChannels{}, Authz: noOrganisations{}, Setup: &fakeSetup{}},
		"sign-up without a session creator": {Sessions: noSessions{}, SignIn: &fakeSignIn{}, Posting: testPoster(), Messages: fakeMessages{}, Channels: &fakeChannels{}, Authz: noOrganisations{}, SignUp: fakeSignUp{&fakeSetup{}}},
	} {
		if _, err := NewHandler("", catalogues, services); err == nil {
			t.Errorf("%s: NewHandler accepted incomplete services", name)
		}
	}
}

func TestSessionLookupOnlyOnRegisteredRoutes(t *testing.T) {
	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		method, path string
		status       int
		lookups      int
	}{
		// Unknown paths and methods: plain 404/405, no lookup, even during
		// an outage.
		{http.MethodGet, "/missing", http.StatusNotFound, 0},
		{http.MethodGet, "/nested/path", http.StatusNotFound, 0},
		{http.MethodPost, "/", http.StatusMethodNotAllowed, 0},
		// Routes outside the HTML middleware never look the session up.
		{http.MethodGet, "/healthz", http.StatusOK, 0},
		{http.MethodGet, "/static/css/app.css", http.StatusOK, 0},
		// A registered page does, so the outage shows as 500 there.
		{http.MethodGet, "/", http.StatusInternalServerError, 1},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			resolver := &countingResolver{}
			handler, err := NewHandler("", catalogues, Services{Sessions: resolver, SignIn: &fakeSignIn{}, Posting: testPoster(), Messages: fakeMessages{}, Channels: &fakeChannels{}, Authz: noOrganisations{}})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "some-token"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status || resolver.calls != tt.lookups {
				t.Fatalf("status %d with %d lookups; want %d with %d", w.Code, resolver.calls, tt.status, tt.lookups)
			}
		})
	}
}
