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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func TestHandler(t *testing.T) {
	bundleURL, err := static.ScriptURL(t.Context(), "message-time")
	if err != nil {
		t.Fatal(err)
	}
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
		{"/readyz", http.StatusServiceUnavailable},
		{"/static/css/app.css", http.StatusOK},
		{bundleURL, http.StatusOK},
		{"/static/dist/manifest.json", http.StatusNotFound},
		{"/static/dist/unknown-ABCDEFGH.js", http.StatusNotFound},
		{"/static/vendor/htmx-2.0.7.min.js", http.StatusOK},
		{"/static/quote-selection-v1.js", http.StatusOK},
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
			if strings.HasPrefix(tt.path, "/static/") && tt.code != http.StatusOK && w.Header().Get("Cache-Control") != "" {
				t.Errorf("missing/private static response is cacheable: %q", w.Header().Get("Cache-Control"))
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

func TestStaticBundleRegistry(t *testing.T) {
	bundleURL, err := static.ScriptURL(t.Context(), "message-time")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"css/app.css", "dist/manifest.json", strings.TrimPrefix(bundleURL, "/static/")} {
		data, err := fs.ReadFile(static.FS(), name)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A real stale bundle isolates registry rejection from the filesystem's 404.
	if err := os.WriteFile(filepath.Join(dir, "dist", "old-ABCDEFGH.js"), []byte("console.log('stale');"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		code int
	}{
		{bundleURL, http.StatusOK},
		{"/static/dist/old-ABCDEFGH.js", http.StatusNotFound},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != tt.code {
				t.Errorf("status = %d, want %d", w.Code, tt.code)
			}
			if tt.code == http.StatusNotFound && w.Header().Get("Cache-Control") != "" {
				t.Errorf("unregistered bundle is cacheable: %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

type fakePinger func(context.Context) error

func (p fakePinger) Ping(ctx context.Context) error { return p(ctx) }

func TestReadiness(t *testing.T) {
	for _, name := range []string{"success", "database down", "timeout", "late success", "shutdown", "shutdown during ping", "nil pinger", "canceled request"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stopping, calls := name == "shutdown", 0
				var pingContext context.Context
				var database Pinger = fakePinger(func(ctx context.Context) error {
					calls++
					pingContext = ctx
					deadline, ok := ctx.Deadline()
					if !ok || deadline.After(time.Now().Add(time.Second)) {
						t.Fatal("ping needs a timeout of at most one second")
					}
					switch name {
					case "database down":
						return errors.New("connection refused: sensitive connection details")
					case "timeout", "canceled request":
						<-ctx.Done()
						return ctx.Err()
					case "late success":
						<-ctx.Done()
					case "shutdown during ping":
						stopping = true
					}
					return nil
				})
				if name == "nil pinger" {
					database = nil
				}
				resolver := &countingResolver{}
				catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
				if err != nil {
					t.Fatal(err)
				}
				handler, err := NewHandler("", catalogues, testServices(func(s *Services) {
					s.Database, s.ShuttingDown, s.Sessions = database, func() bool { return stopping }, resolver
				}))
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"/readyz", "/healthz"} {
					r := httptest.NewRequest(http.MethodGet, path, nil)
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "some-token"})
					if name == "canceled request" {
						ctx, cancel := context.WithCancel(r.Context())
						cancel()
						r = r.WithContext(ctx)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					status, body := http.StatusOK, "ok\n"
					if path == "/readyz" && name != "success" {
						status, body = http.StatusServiceUnavailable, "unavailable\n"
					}
					if w.Code != status || w.Body.String() != body {
						t.Errorf("%s: status = %d, body = %q; want %d, %q", path, w.Code, w.Body.String(), status, body)
					}
					for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cross-Origin-Opener-Policy", "Permissions-Policy", "Set-Cookie", "Vary", "Retry-After"} {
						if value := w.Header().Get(header); value != "" {
							t.Errorf("%s: unexpected %s = %q", path, header, value)
						}
					}
				}
				wantCalls := 1
				if name == "shutdown" || name == "nil pinger" {
					wantCalls = 0
				}
				if calls != wantCalls || resolver.calls != 0 {
					t.Errorf("ping calls = %d, session lookups = %d; want %d, 0", calls, resolver.calls, wantCalls)
				}
				if pingContext != nil && pingContext.Err() == nil {
					t.Error("ping context was not released")
				}
			})
		})
	}
}

// TestStaticHeaders: static files bypass SecurityHeaders, so the static
// handler sends its own nosniff and CSP on every response, found or not,
// and refuses methods other than GET and HEAD with 405 and Allow,
// from the embedded assets and from a development directory.
func TestStaticHeaders(t *testing.T) {
	for _, dir := range []string{"", filepath.Join("..", "..", "web", "static")} {
		handler, err := newTestHandler(t, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			method, path, rangeHeader string
			code                      int
		}{
			{http.MethodGet, "/static/brand/logo-v1.svg", "", http.StatusOK},
			{http.MethodGet, "/static/channel-images-v1/lake.svg", "", http.StatusOK},
			{http.MethodGet, "/static/arrival-v1.js", "", http.StatusOK},
			{http.MethodGet, "/static/css/app.css", "", http.StatusOK},
			{http.MethodHead, "/static/brand/logo-v1.svg", "", http.StatusOK},
			{http.MethodGet, "/static/brand/logo-v1.svg", "bytes=0-9", http.StatusPartialContent},
			{http.MethodGet, "/static/missing.svg", "", http.StatusNotFound},
			{http.MethodPost, "/static/brand/logo-v1.svg", "", http.StatusMethodNotAllowed},
			{http.MethodOptions, "/static/brand/logo-v1.svg", "", http.StatusMethodNotAllowed},
			{http.MethodPost, "/static/missing.svg", "", http.StatusMethodNotAllowed},
		} {
			t.Run(fmt.Sprintf("dev=%t/%s %s %s", dir != "", tt.method, tt.path, tt.rangeHeader), func(t *testing.T) {
				r := httptest.NewRequest(tt.method, tt.path, nil)
				if tt.rangeHeader != "" {
					r.Header.Set("Range", tt.rangeHeader)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != tt.code {
					t.Fatalf("status = %d, want %d", w.Code, tt.code)
				}
				if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("X-Content-Type-Options = %q", got)
				}
				if got := w.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
					t.Errorf("Content-Security-Policy = %q", got)
				}
				for _, header := range []string{"Cross-Origin-Opener-Policy", "Permissions-Policy"} {
					if got := w.Header().Get(header); got != "" {
						t.Errorf("unexpected %s = %q", header, got)
					}
				}
				if tt.code == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
					t.Errorf("Allow = %q", w.Header().Get("Allow"))
				}
			})
		}
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
				// The layout's arrival script, htmx with its two extensions and
				// the popover focus script.
				if len(tags) != 5 {
					t.Fatalf("got %d scripts, want 5", len(tags))
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
				var settings map[string]json.RawMessage
				if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &settings); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"allowEval", "allowScriptTags", "includeIndicatorStyles"} {
					if value := settings[name]; string(bytes.TrimSpace(value)) != "false" {
						t.Errorf("htmx %s must explicitly be false", name)
					}
				}
				var handling []map[string]any
				if err := json.Unmarshal(settings["responseHandling"], &handling); err != nil {
					t.Fatal(err)
				}
				// htmx uses the first match: retain defaults, with 409 and 422
				// before the catch-all error rule so field errors can swap.
				wantHandling := []map[string]any{
					{"code": "204", "swap": false},
					{"code": "[23]..", "swap": true},
					{"code": "409", "swap": true, "error": false},
					{"code": "422", "swap": true, "error": false},
					{"code": "[45]..", "swap": false, "error": true},
				}
				if len(handling) != len(wantHandling) {
					t.Fatalf("htmx responseHandling = %v, want %v", handling, wantHandling)
				}
				for i, want := range wantHandling {
					if len(handling[i]) != len(want) {
						t.Errorf("htmx responseHandling[%d] = %v, want %v", i, handling[i], want)
					}
					for key, value := range want {
						if handling[i][key] != value {
							t.Errorf("htmx responseHandling[%d].%s = %v, want %v", i, key, handling[i][key], value)
						}
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
		"Content-Security-Policy":    "default-src 'self'; script-src 'nonce-" + nonce + "'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
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
	builder := filepath.Join(t.TempDir(), "webbuild")
	if out, err := exec.Command("go", "-C", "../../tools", "build", "-o", builder, "./webbuild").CombinedOutput(); err != nil {
		t.Fatalf("building webbuild: %v: %s", err, out)
	}
	root := t.TempDir()
	staticDir := filepath.Join(root, "web", "static")
	devDir := filepath.Join(root, "bin", "dev-dist")
	registry, err := os.ReadFile("../../web/static/scripts.go")
	if err != nil || os.CopyFS(filepath.Join(root, "web", "src"), os.DirFS("../../web/src")) != nil || os.MkdirAll(filepath.Join(staticDir, "css"), 0o755) != nil || os.WriteFile(filepath.Join(staticDir, "scripts.go"), registry, 0o600) != nil {
		t.Fatal("preparing fixture:", err)
	}
	cssPath := filepath.Join(staticDir, "css", "app.css")
	initialCSS := "body { color: red; }"
	if err := os.WriteFile(cssPath, []byte(initialCSS), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(builder, root, "commit", "run").CombinedOutput(); err != nil {
		t.Fatalf("initial production build: %v: %s", err, out)
	}
	prodDistDir := filepath.Join(staticDir, "dist")
	prodBefore := snapshotFiles(t, prodDistDir)
	if out, err := exec.Command(builder, "-dev", root).CombinedOutput(); err != nil {
		t.Fatalf("initial dev build: %v: %s", err, out)
	}
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	setup := &fakeSetup{open: true}
	handler, err := NewHandler(devDir, catalogues, testServices(asAlice, func(s *Services) {
		s.Setup, s.SetupSessions, s.SignUp = setup, setup, fakeSignUp{&fakeSetup{open: true}}
	}))
	prodHandler, prodErr := newTestHandler(t, "")
	prodScriptURL, urlErr := static.ScriptURL(t.Context(), "message-time")
	if err != nil || prodErr != nil || urlErr != nil {
		t.Fatal(err, prodErr, urlErr)
	}
	scriptPattern := regexp.MustCompile(`/static/dist/message-time-[A-Z0-9]{8}\.js`)
	previousCSSURL, previousScriptURL := "", ""
	for i, css := range []string{initialCSS, "body { color: blue; }"} {
		if os.WriteFile(cssPath, []byte(css), 0o600) != nil || os.WriteFile(filepath.Join(root, "web", "src", "message-time.ts"), fmt.Appendf(nil, "console.log(%q);\n", fmt.Sprintf("edit-%d", i)), 0o600) != nil {
			t.Fatal("writing updated sources")
		}
		if out, err := exec.Command(builder, "-dev", root).CombinedOutput(); err != nil {
			t.Fatalf("rebuilding dev bundles: %v: %s", err, out)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/signup", nil))
		wantCSSURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256([]byte(css)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<link rel="stylesheet" href="`+wantCSSURL+`">`) || (previousCSSURL != "" && strings.Contains(w.Body.String(), previousCSSURL)) {
			t.Fatalf("page does not link only to current CSS: status = %d, body = %s", w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wantCSSURL, nil))
		if w.Code != http.StatusOK || w.Body.String() != css || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("stylesheet response: status = %d, Cache-Control = %q, body = %q", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
		previousCSSURL = wantCSSURL

		req := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", kernel.ID{1}), nil)
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		scriptURL := scriptPattern.FindString(w.Body.String())
		if w.Code != http.StatusOK || scriptURL == "" || scriptURL == previousScriptURL || (previousScriptURL != "" && strings.Contains(w.Body.String(), previousScriptURL)) {
			t.Fatalf("channel page does not link only to current bundle (%q vs %q): status = %d", scriptURL, previousScriptURL, w.Code)
		}
		for _, assetURL := range []string{scriptURL, scriptURL + ".map"} {
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, assetURL, nil))
			if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("GET %s: status = %d, Cache-Control = %q", assetURL, w.Code, w.Header().Get("Cache-Control"))
			}
		}
		// A page rendered just before a rebuild switch can still fetch its bundle and map, while an unknown hash stays 404.
		if previousScriptURL != "" {
			for _, retainedURL := range []string{previousScriptURL, previousScriptURL + ".map"} {
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, retainedURL, nil))
				if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("GET retained %s after switch: status = %d, Cache-Control = %q", retainedURL, w.Code, w.Header().Get("Cache-Control"))
				}
			}
		}
		for _, unknownURL := range []string{"/static/dist/message-time-00000000.js", "/static/dist/message-time-00000000.js.map"} {
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, unknownURL, nil))
			if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "" {
				t.Errorf("GET never-registered %s: status = %d, Cache-Control = %q", unknownURL, w.Code, w.Header().Get("Cache-Control"))
			}
		}
		previousScriptURL = scriptURL
	}

	// Failed rebuild leaves the previous generation fully servable; a subsequent valid build recovers.
	if err := os.WriteFile(filepath.Join(root, "web", "src", "message-time.ts"), []byte("const broken = ;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(builder, "-dev", root).Run(); err == nil {
		t.Fatal("expected broken TypeScript dev build to fail")
	}
	for _, url := range []string{previousScriptURL, previousScriptURL + ".map"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s after failed rebuild: status = %d", url, w.Code)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "web", "src", "message-time.ts"), []byte("console.log('recovered');\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(builder, "-dev", root).CombinedOutput(); err != nil {
		t.Fatalf("recovery dev build: %v: %s", err, out)
	}
	req := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", kernel.ID{1}), nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	recoveredURL := scriptPattern.FindString(w.Body.String())
	if w.Code != http.StatusOK || recoveredURL == "" || recoveredURL == previousScriptURL {
		t.Fatalf("recovery page did not switch bundle: got %q, prev %q", recoveredURL, previousScriptURL)
	}
	for _, url := range []string{recoveredURL, recoveredURL + ".map", previousScriptURL} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s after recovery: status = %d", url, w.Code)
		}
	}

	// Concurrent reader loop of page + bundle + map while rebuilds run.
	stopRead := make(chan struct{})
	readErr := make(chan error, 1)
	var cycles atomic.Int64
	go func() {
		defer close(readErr)
		for {
			select {
			case <-stopRead:
				return
			default:
			}
			r := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", kernel.ID{1}), nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			u := scriptPattern.FindString(rec.Body.String())
			if rec.Code != http.StatusOK || u == "" {
				readErr <- fmt.Errorf("concurrent page render: status = %d, url = %q", rec.Code, u)
				return
			}
			for _, asset := range []string{u, u + ".map"} {
				ar := httptest.NewRecorder()
				handler.ServeHTTP(ar, httptest.NewRequest(http.MethodGet, asset, nil))
				if ar.Code != http.StatusOK || ar.Body.Len() == 0 {
					readErr <- fmt.Errorf("concurrent GET %s: status = %d, len = %d", asset, ar.Code, ar.Body.Len())
					return
				}
			}
			cycles.Add(1)
		}
	}()
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(root, "web", "src", "message-time.ts"), fmt.Appendf(nil, "console.log('concurrent-%d');\n", i), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(builder, "-dev", root).CombinedOutput(); err != nil {
			t.Fatalf("concurrent dev build %d: %v: %s", i, err, out)
		}
	}
	target := cycles.Load() + 5
	for cycles.Load() < target {
		select {
		case err := <-readErr:
			t.Fatalf("concurrent HTTP reader failed: %v", err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(stopRead)
	if err := <-readErr; err != nil {
		t.Fatalf("concurrent HTTP reader error: %v", err)
	}

	if got := snapshotFiles(t, prodDistDir); got != prodBefore {
		t.Fatalf("development builds modified web/static/dist:\ngot:\n%s\nwant:\n%s", got, prodBefore)
	}
	w = httptest.NewRecorder()
	prodHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, prodScriptURL, nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("production bundle: status = %d, Cache-Control = %q", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, mapURL := range []string{prodScriptURL + ".map", recoveredURL + ".map"} {
		w = httptest.NewRecorder()
		prodHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, mapURL, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("production GET %s: status = %d, want 404", mapURL, w.Code)
		}
	}
}

func snapshotFiles(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s:%x\n", entry.Name(), sha256.Sum256(data))
	}
	return b.String()
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
	return NewHandler(dir, catalogues, testServices(func(s *Services) {
		s.Setup, s.SetupSessions, s.SignUp = setup, setup, fakeSignUp{&fakeSetup{open: true}}
	}))
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

func (noOrganisations) Member(context.Context, *identity.Account, string) (org.Membership, error) {
	return org.Membership{}, org.ErrNotFound
}

func (noOrganisations) HomeSlug(context.Context, *identity.Account) (string, error) {
	return "", org.ErrNotFound
}

// noSessions signs nobody in, for tests that do not need a session.
type noSessions struct{}

func (noSessions) Resolve(context.Context, string) (identity.Account, identity.Session, error) {
	return identity.Account{}, identity.Session{}, identity.ErrNoSession
}

// countingResolver counts lookups and fails every one, like a database
// outage.
type countingResolver struct{ calls int }

func (c *countingResolver) Resolve(context.Context, string) (identity.Account, identity.Session, error) {
	c.calls++
	return identity.Account{}, identity.Session{}, errors.New("connection refused")
}

func TestNewHandlerRequiresServices(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The defaults are complete, and each case lacks only the named service,
	// so it still fails for that reason alone when another becomes required.
	if _, err := NewHandler("", catalogues, testServices()); err != nil {
		t.Fatalf("NewHandler rejected the default services: %v", err)
	}
	for name, missing := range map[string]func(*Services){
		"stream without lifetime":           func(s *Services) { s.Stream = &Streaming{} },
		"missing member limits":             func(s *Services) { s.MemberLimits = MemberLimits{} },
		"preferences missing":               func(s *Services) { s.Preferences = nil },
		"no authorizer":                     func(s *Services) { s.Authz = nil },
		"no channels":                       func(s *Services) { s.Channels = nil },
		"no topics":                         func(s *Services) { s.Topics = nil },
		"no messages":                       func(s *Services) { s.Messages = nil },
		"no posting":                        func(s *Services) { s.Posting = nil },
		"typed-nil posting":                 func(s *Services) { s.Posting = (*conversation.Posting)(nil) },
		"no sessions":                       func(s *Services) { s.Sessions = nil },
		"no sign-in":                        func(s *Services) { s.SignIn = nil },
		"setup without a session creator":   func(s *Services) { s.Setup = &fakeSetup{} },
		"sign-up without a session creator": func(s *Services) { s.SignUp = fakeSignUp{&fakeSetup{}} },
	} {
		if _, err := NewHandler("", catalogues, testServices(missing)); err == nil {
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
		{http.MethodGet, "/readyz", http.StatusServiceUnavailable, 0},
		{http.MethodGet, "/static/css/app.css", http.StatusOK, 0},
		// A registered page does, so the outage shows as 500 there.
		{http.MethodGet, "/", http.StatusInternalServerError, 1},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			resolver := &countingResolver{}
			handler, err := NewHandler("", catalogues, testServices(func(s *Services) { s.Sessions = resolver }))
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

// Disk fixtures need the same complete generation as a development checkout.
func copyScriptAssets(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(static.FS(), "dist/*")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := fs.ReadFile(static.FS(), name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandlerRejectsIncompleteScripts(t *testing.T) {
	for _, tc := range []struct{ name, manifest, want string }{
		{"missing declaration", `{"entries":{}}`, "missing declared entry"},
		{"missing bundle", `{"entries":{"message-time":{"file":"message-time-ABCDEFGH.js"}}}`, "reading script bundle"},
		{"unknown entry", "", "unknown entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			copyScriptAssets(t, dir)
			if err := os.Mkdir(filepath.Join(dir, "css"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "css/app.css"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			data := []byte(tc.manifest)
			if tc.name == "unknown entry" {
				original, err := fs.ReadFile(static.FS(), "dist/manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				var manifest map[string]any
				if err := json.Unmarshal(original, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest["entries"].(map[string]any)["unknown"] = map[string]any{}
				data, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "dist/manifest.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := newTestHandler(t, dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("startup: want %s, got %v", tc.want, err)
			}
		})
	}
}
