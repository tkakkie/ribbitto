package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/a-h/templ"
)

func TestPageRendererStylesheetError(t *testing.T) {
	for _, tt := range []struct {
		name            string
		stylesheetURL   func() (string, error)
		status          int
		failOnPage      bool
		wantCode        int
		wantBody        string
		wantContentType string
		wantNosniff     string
	}{
		{
			name:            "stylesheet error",
			stylesheetURL:   func() (string, error) { return "", errors.New("css gone") },
			status:          http.StatusCreated,
			failOnPage:      true,
			wantCode:        http.StatusInternalServerError,
			wantBody:        "Internal Server Error\n",
			wantContentType: "text/plain; charset=utf-8",
			wantNosniff:     "nosniff",
		},
		{
			name:          "renders component with url and status",
			stylesheetURL: func() (string, error) { return "/static/css/app.css?v=abc", nil },
			status:        http.StatusTooManyRequests,
			wantCode:      http.StatusTooManyRequests,
			wantBody:      "/static/css/app.css?v=abc",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &pageRenderer{stylesheetURL: tt.stylesheetURL}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			p.render(w, r, tt.status, func(url string) templ.Component {
				if tt.failOnPage {
					t.Fatalf("%s: page function called with url = %q, want not called", tt.name, url)
				}
				return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
					_, err := io.WriteString(out, url)
					return err
				})
			})
			if w.Code != tt.wantCode {
				t.Errorf("%s: status = %d, want %d", tt.name, w.Code, tt.wantCode)
			}
			if got := w.Body.String(); got != tt.wantBody {
				t.Errorf("%s: body = %q, want %q", tt.name, got, tt.wantBody)
			}
			if tt.wantContentType != "" {
				if got := w.Header().Get("Content-Type"); got != tt.wantContentType {
					t.Errorf("%s: Content-Type = %q, want %q", tt.name, got, tt.wantContentType)
				}
			}
			if tt.wantNosniff != "" {
				if got := w.Header().Get("X-Content-Type-Options"); got != tt.wantNosniff {
					t.Errorf("%s: X-Content-Type-Options = %q, want %q", tt.name, got, tt.wantNosniff)
				}
			}
		})
	}
}

func TestStylesheetURLForAssets(t *testing.T) {
	data := []byte("a{}")
	wantURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256(data))
	otherData := []byte("b{}")
	wantOtherURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256(otherData))

	for _, tt := range []struct {
		name          string
		assets        fstest.MapFS
		wantURL       string
		forbidURL     string
		wantErr       error
		wantErrPrefix string
	}{
		{
			name:          "missing stylesheet",
			assets:        fstest.MapFS{},
			wantErr:       fs.ErrNotExist,
			wantErrPrefix: "reading stylesheet: ",
		},
		{
			name:    "hashes stylesheet content",
			assets:  fstest.MapFS{"css/app.css": &fstest.MapFile{Data: data}},
			wantURL: wantURL,
		},
		{
			name:      "different content gives different url",
			assets:    fstest.MapFS{"css/app.css": &fstest.MapFile{Data: otherData}},
			wantURL:   wantOtherURL,
			forbidURL: wantURL,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			url, err := stylesheetURLForAssets(tt.assets)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("%s: errors.Is(%v, %v) = false, want true", tt.name, err, tt.wantErr)
				}
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErrPrefix) {
					t.Errorf("%s: err = %v, want prefix %q", tt.name, err, tt.wantErrPrefix)
				}
			} else if err != nil {
				t.Errorf("%s: err = %v, want nil", tt.name, err)
			}
			if url != tt.wantURL {
				t.Errorf("%s: url = %q, want %q", tt.name, url, tt.wantURL)
			}
			if tt.forbidURL != "" && url == tt.forbidURL {
				t.Errorf("%s: url = %q, want different from %q", tt.name, url, tt.forbidURL)
			}
		})
	}
}

func TestNewHandlerStylesheetMissing(t *testing.T) {
	handler, err := newTestHandler(t, t.TempDir())
	if handler != nil {
		t.Errorf("TestNewHandlerStylesheetMissing: handler = %v, want nil", handler)
	}
	if err == nil || !strings.Contains(err.Error(), "reading stylesheet") {
		t.Errorf("TestNewHandlerStylesheetMissing: err = %v, want substring %q", err, "reading stylesheet")
	}
}

func TestDevelopmentStylesheetRemoved(t *testing.T) {
	dir := t.TempDir()
	copyScriptAssets(t, dir)
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

	t.Run("removed stylesheet", func(t *testing.T) {
		if err := os.Remove(cssPath); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("removed stylesheet: status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
		if got := w.Body.String(); got != "Internal Server Error\n" {
			t.Errorf("removed stylesheet: body = %q, want %q", got, "Internal Server Error\n")
		}
	})

	t.Run("restored stylesheet", func(t *testing.T) {
		if err := os.WriteFile(cssPath, []byte(initialCSS), 0o600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Errorf("restored stylesheet: status = %d, want %d", w.Code, http.StatusOK)
		}
	})
}
