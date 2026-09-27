package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// NewHandler constructs the application's HTTP routes. A non-empty devAssets
// directory serves live assets from disk instead of the embedded production assets.
func NewHandler(devAssets string, catalogues *i18n.Catalogues, sessions middleware.SessionResolver) (http.Handler, error) {
	assets := static.FS()
	if devAssets != "" {
		assets = os.DirFS(devAssets)
	}
	stylesheetURL, err := stylesheetURLForAssets(assets)
	if err != nil {
		return nil, err
	}
	var home http.Handler = templ.Handler(view.Hello(stylesheetURL))
	if devAssets != "" {
		home = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			url, err := stylesheetURLForAssets(assets)
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			templ.Handler(view.Hello(url)).ServeHTTP(w, r)
		})
	}
	// Register HTML routes here so new pages inherit the shared middleware.
	pages := sessionMux{http.NewServeMux(), sessions}
	pages.Handle("GET /{$}", home)
	mux := http.NewServeMux()
	mux.Handle("/", middleware.SecurityHeaders(catalogues.Middleware(pages)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only successful file responses are immutable; missing assets may appear later.
		info, err := fs.Stat(assets, r.URL.Path)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if devAssets != "" {
			w.Header().Set("Cache-Control", "no-store")
		}
		http.FileServerFS(assets).ServeHTTP(w, r)
	})))
	// Outermost: reject cross-origin state changes before anything else runs.
	// It lets GET, HEAD and OPTIONS through, so every route that changes
	// state must be a POST.
	return http.NewCrossOriginProtection().Handler(middleware.LimitBody(mux)), nil
}

func stylesheetURLForAssets(assets fs.FS) (string, error) {
	css, err := fs.ReadFile(assets, "css/app.css")
	if err != nil {
		return "", fmt.Errorf("reading stylesheet: %w", err)
	}
	return fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256(css)), nil
}

// sessionMux registers HTML routes behind the session middleware one by
// one, rather than wrapping the whole mux: only a registered route looks the
// session up, so an unknown path answers 404 without a database query (and
// still 404, not 500, while the database is down).
type sessionMux struct {
	*http.ServeMux
	sessions middleware.SessionResolver
}

// Handle registers handler for pattern behind the session middleware.
func (m sessionMux) Handle(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern, middleware.Session(m.sessions, handler))
}

// HandleFunc registers f for pattern behind the session middleware.
func (m sessionMux) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(f))
}
