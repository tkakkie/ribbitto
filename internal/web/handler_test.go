package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/web/static"
)

func TestHandler(t *testing.T) {
	handler, err := NewHandler("")
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

func TestHello(t *testing.T) {
	handler, err := NewHandler("")
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
	for _, file := range []string{"htmx-2.0.7.min.js", "htmx-ext-sse-2.2.4.min.js", "idiomorph-ext-0.7.4.min.js"} {
		index := strings.Index(body, `<script defer src="/static/vendor/`+file+`"></script>`)
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
	handler, err := NewHandler(dir)
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
