package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// NewHandler constructs the application's HTTP routes from embedded assets.
func NewHandler() (http.Handler, error) {
	assets := static.FS()
	css, err := fs.ReadFile(assets, "css/app.css")
	if err != nil {
		return nil, fmt.Errorf("reading embedded stylesheet: %w", err)
	}
	stylesheetURL := fmt.Sprintf("/static/css/app.css?v=%x", sha256.Sum256(css))
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", templ.Handler(view.Hello(stylesheetURL)))
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
		http.FileServerFS(assets).ServeHTTP(w, r)
	})))
	return mux, nil
}
