package web

import (
	"crypto/sha256"
	"errors"
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

// Services are the use cases the handlers call.
type Services struct {
	Sessions      middleware.SessionResolver
	SignIn        SignInService
	SignUp        SignUpService
	Setup         SetupService // nil disables both setup routes
	SetupSessions SessionCreator
}

// NewHandler constructs the application's HTTP routes. A non-empty devAssets
// directory serves live assets from disk instead of the embedded production assets.
func NewHandler(devAssets string, catalogues *i18n.Catalogues, services Services) (http.Handler, error) {
	// Fail at start-up rather than panic on the first request.
	if services.Sessions == nil || services.SignIn == nil {
		return nil, errors.New("web: Services.Sessions and Services.SignIn are required")
	}
	if (services.Setup != nil || services.SignUp != nil) && services.SetupSessions == nil {
		return nil, errors.New("web: Services.SetupSessions is required when Services.Setup or Services.SignUp is set")
	}
	assets := static.FS()
	if devAssets != "" {
		assets = os.DirFS(devAssets)
	}
	stylesheetURL, err := stylesheetURLForAssets(assets)
	if err != nil {
		return nil, err
	}
	pages := &pageRenderer{stylesheetURL: func() (string, error) { return stylesheetURL, nil }}
	if devAssets != "" {
		pages.stylesheetURL = func() (string, error) { return stylesheetURLForAssets(assets) }
	}
	// Register HTML routes here so new pages inherit the shared middleware.
	routes := sessionMux{http.NewServeMux(), services.Sessions}
	routes.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		account, signedIn := middleware.Account(r.Context())
		pages.render(w, r, http.StatusOK, func(url string) templ.Component {
			return view.Hello(url, view.Viewer{SignedIn: signedIn, DisplayName: account.DisplayName})
		})
	})
	registerSignIn(routes, pages, services.SignIn, services.SignUp)
	registerSetup(routes, pages, services.Setup, services.SetupSessions)
	registerSignUp(routes, pages, services.SignUp, services.SetupSessions)
	mux := http.NewServeMux()
	mux.Handle("/", middleware.SecurityHeaders(catalogues.Middleware(routes)))
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

// pageRenderer renders full pages; in development the stylesheet URL is
// recomputed per request so rebuilt CSS shows up without a restart.
type pageRenderer struct {
	stylesheetURL func() (string, error)
}

func (p *pageRenderer) render(w http.ResponseWriter, r *http.Request, status int, page func(stylesheetURL string) templ.Component) {
	url, err := p.stylesheetURL()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	templ.Handler(page(url), templ.WithStatus(status)).ServeHTTP(w, r)
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
