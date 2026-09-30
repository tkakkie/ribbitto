package web

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
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
	SetupSessions SessionReplacer
	Authz         Authorizer
	Messages      MessageReader
	Posting       *message.Service
	Channels      ChannelService
	Limits        *middleware.AuthLimits // nil: no rate limits (tests)
	Stream        *Streaming             // nil: the channel event stream answers 404
}

// NewHandler constructs the application's HTTP routes. A non-empty devAssets
// directory serves live assets from disk instead of the embedded production assets.
func NewHandler(devAssets string, catalogues *i18n.Catalogues, services Services) (http.Handler, error) {
	handler, _, err := newHandler(devAssets, catalogues, services)
	return handler, err
}

// newHandler is NewHandler that also returns every HTML route pattern it
// registered, so the markup tests can prove each page has a rendered case.
func newHandler(devAssets string, catalogues *i18n.Catalogues, services Services) (http.Handler, []string, error) {
	// Fail at start-up rather than panic on the first request.
	if services.Sessions == nil || services.SignIn == nil || services.Authz == nil || services.Channels == nil || services.Messages == nil || services.Posting == nil {
		return nil, nil, errors.New("web: Services.Sessions, Services.SignIn, Services.Authz, Services.Channels, Services.Messages and Services.Posting are required")
	}
	if (services.Setup != nil || services.SignUp != nil) && services.SetupSessions == nil {
		return nil, nil, errors.New("web: Services.SetupSessions is required when Services.Setup or Services.SignUp is set")
	}
	assets := static.FS()
	if devAssets != "" {
		assets = os.DirFS(devAssets)
	}
	stylesheetURL, err := stylesheetURLForAssets(assets)
	if err != nil {
		return nil, nil, err
	}
	pages := &pageRenderer{stylesheetURL: func() (string, error) { return stylesheetURL, nil }}
	if devAssets != "" {
		pages.stylesheetURL = func() (string, error) { return stylesheetURLForAssets(assets) }
	}
	// Register HTML routes here so new pages inherit the shared middleware.
	routes := sessionMux{http.NewServeMux(), services.Sessions, &[]string{}}
	routes.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		account, signedIn := middleware.Account(r.Context())
		if signedIn {
			slug, err := services.Authz.HomeSlug(r.Context(), &account)
			switch {
			case err == nil:
				http.Redirect(w, r, "/organizations/"+slug+"/", http.StatusSeeOther)
				return
			case !errors.Is(err, authz.ErrNotFound):
				serverError(w, r, "finding home organisation", err)
				return
			}
		}
		viewer := view.Viewer{SignedIn: signedIn, DisplayName: account.DisplayName}
		if !signedIn && services.SignUp != nil {
			open, err := services.SignUp.Open(r.Context())
			if err != nil {
				serverError(w, r, "checking sign-up", err)
				return
			}
			viewer.SignUpOpen = open
		}
		pages.render(w, r, http.StatusOK, func(url string) templ.Component { return view.Hello(url, viewer) })
	})
	// limit returns the rate-limit check for one form. Handlers call it after
	// deciding the route is open (a closed route stays 404) and before
	// parsing or hashing anything.
	limit := func(pick func(*middleware.AuthLimits) *middleware.RateLimiter) func(http.ResponseWriter, *http.Request) bool {
		return func(w http.ResponseWriter, r *http.Request) bool {
			if services.Limits == nil || services.Limits.Allow(pick(services.Limits), r) {
				return true
			}
			pages.render(w, r, http.StatusTooManyRequests, view.TooManyRequests)
			return false
		}
	}
	registerSignIn(routes, pages, services.SignIn, services.SignUp, limit(func(l *middleware.AuthLimits) *middleware.RateLimiter { return l.SignIn }))
	registerSetup(routes, pages, services.Setup, services.SetupSessions, limit(func(l *middleware.AuthLimits) *middleware.RateLimiter { return l.Setup }))
	registerSignUp(routes, pages, services.SignUp, services.SetupSessions, limit(func(l *middleware.AuthLimits) *middleware.RateLimiter { return l.SignUp }))
	registerOrgRoutes(routes, services.Authz, orgRoutes(pages, services.Channels, services.Messages, services.Posting, services.Stream))
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
		// Serve a request for several ranges in full. Nothing here needs one,
		// and each part carries its own multipart headers, so many tiny
		// ranges would multiply the response. Defense in depth next to the
		// server's write timeout, not a replacement for it.
		if strings.Contains(r.Header.Get("Range"), ",") {
			r.Header.Del("Range")
		}
		http.FileServerFS(assets).ServeHTTP(w, r)
	})))
	// Outermost: reject cross-origin state changes before anything else runs.
	// It lets GET, HEAD and OPTIONS through, so every route that changes
	// state must be a POST.
	return http.NewCrossOriginProtection().Handler(middleware.LimitBody(mux)), *routes.patterns, nil
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
	patterns *[]string // every HTML route registered, for the markup tests
}

// Handle registers handler for pattern behind the session middleware.
func (m sessionMux) Handle(pattern string, handler http.Handler) {
	*m.patterns = append(*m.patterns, pattern)
	m.ServeMux.Handle(pattern, middleware.Session(m.sessions, handler))
}

// HandleFuncWithoutSession registers an HTML route that must decide whether
// it is open before any session lookup (setup, sign-up).
func (m sessionMux) HandleFuncWithoutSession(pattern string, f func(http.ResponseWriter, *http.Request)) {
	*m.patterns = append(*m.patterns, pattern)
	m.ServeMux.HandleFunc(pattern, f)
}

// HandleFunc registers f for pattern behind the session middleware.
func (m sessionMux) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(f))
}
