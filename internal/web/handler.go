package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Services are the use cases the handlers call.
type Services struct {
	RequestLogger *slog.Logger // nil: use the process logger
	Database      Pinger       // nil: readiness answers 503
	ShuttingDown  func() bool  // nil: no shutdown signal (tests)
	MemberSearch  MemberSearch // nil: mention candidates answer 404
	MentionLimits *middleware.MemberRateLimiter
	Typing        *typing.State
	SignalLimits  *middleware.MemberRateLimiter
	ReadLimits    *middleware.MemberRateLimiter
	CreateLimits  *middleware.MemberRateLimiter
	ResyncLimits  *middleware.MemberRateLimiter
	BranchLimits  *middleware.MemberRateLimiter
	PostLimits    *middleware.MemberRateLimiter
	PinLimits     *middleware.MemberRateLimiter
	Sessions      middleware.SessionResolver
	SignIn        SignInService
	SignUp        SignUpService
	Setup         SetupService // nil disables both setup routes
	SetupSessions SessionReplacer
	Authz         Authorizer
	Topics        TopicLookup
	Messages      MessageReader
	Posting       *conversation.Posting
	Branching     Branching // nil: branching answers 404
	Pinning       Pinning   // nil: pin and unpin answer 404
	Channels      Channels
	Limits        *middleware.AuthLimits // nil: no authentication rate limits (tests)
	SidebarResync SidebarResync          // nil: resync POSTs answer 404
	Reading       Reading                // nil: read POSTs answer 404
	Stream        *Streaming             // nil: the channel event stream answers 404
}

// staticContentSecurityPolicy applies to every /static/ response.
const staticContentSecurityPolicy = "default-src 'none'; sandbox"

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
	if services.Sessions == nil || services.SignIn == nil || services.Authz == nil || services.Channels == nil || services.Topics == nil || services.Messages == nil || services.Posting == nil {
		return nil, nil, errors.New("web: Services.Sessions, Services.SignIn, Services.Authz, Services.Channels, Services.Topics, Services.Messages and Services.Posting are required")
	}
	if (services.Setup != nil || services.SignUp != nil) && services.SetupSessions == nil {
		return nil, nil, errors.New("web: Services.SetupSessions is required when Services.Setup or Services.SignUp is set")
	}
	if services.Stream != nil && services.Stream.Lifetime == nil {
		return nil, nil, errors.New("web: Streaming.Lifetime is required")
	}
	if services.Typing != nil && services.SignalLimits == nil {
		return nil, nil, errors.New("web: Services.SignalLimits is required with Typing")
	}
	if services.Reading != nil && (services.ReadLimits == nil || services.ReadLimits == services.SignalLimits) {
		return nil, nil, errors.New("web: independent Services.ReadLimits is required with Reading")
	}
	if services.Channels != nil && (services.CreateLimits == nil || services.CreateLimits == services.ReadLimits || services.CreateLimits == services.SignalLimits) {
		return nil, nil, errors.New("web: independent Services.CreateLimits is required with Channels")
	}
	if services.SidebarResync != nil && (services.ResyncLimits == nil || services.ResyncLimits == services.SignalLimits || services.ResyncLimits == services.ReadLimits || services.ResyncLimits == services.CreateLimits) {
		return nil, nil, errors.New("web: independent Services.ResyncLimits is required with SidebarResync")
	}
	if services.Branching != nil && (services.BranchLimits == nil || services.BranchLimits == services.SignalLimits || services.BranchLimits == services.ReadLimits || services.BranchLimits == services.CreateLimits || services.BranchLimits == services.ResyncLimits) {
		return nil, nil, errors.New("web: independent Services.BranchLimits is required with Branching")
	}
	if services.PostLimits == nil || services.PostLimits == services.SignalLimits || services.PostLimits == services.ReadLimits || services.PostLimits == services.CreateLimits || services.PostLimits == services.ResyncLimits || services.PostLimits == services.BranchLimits {
		return nil, nil, errors.New("web: independent Services.PostLimits is required")
	}
	if services.PinLimits == nil || services.PinLimits == services.SignalLimits || services.PinLimits == services.ReadLimits || services.PinLimits == services.CreateLimits || services.PinLimits == services.ResyncLimits || services.PinLimits == services.BranchLimits || services.PinLimits == services.PostLimits {
		return nil, nil, errors.New("web: independent Services.PinLimits is required")
	}
	if services.MemberSearch != nil && (services.MentionLimits == nil || services.MentionLimits == services.SignalLimits || services.MentionLimits == services.ReadLimits || services.MentionLimits == services.CreateLimits || services.MentionLimits == services.ResyncLimits || services.MentionLimits == services.BranchLimits) {
		return nil, nil, errors.New("web: independent Services.MentionLimits is required with MemberSearch")
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
			case !errors.Is(err, org.ErrNotFound):
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
	registerOrgRoutes(routes, services.Authz, orgRoutes(pages, services.Channels, services.Topics, services.Messages, services.Posting, services.Branching, services.Pinning, services.Stream, services.Reading, services.Typing, services.SignalLimits, services.ReadLimits, services.CreateLimits, services.SidebarResync, services.ResyncLimits, services.BranchLimits, services.PostLimits, services.PinLimits, services.MemberSearch, services.MentionLimits))
	mux := http.NewServeMux()
	mux.Handle("/", middleware.SecurityHeaders(catalogues.Middleware(routes)))
	mux.Handle("GET /healthz", middleware.LogRoute("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})))
	mux.Handle("GET /readyz", middleware.LogRoute("GET /readyz", readiness(services.Database, services.ShuttingDown)))
	// Every method reaches this handler, so even a refusal carries the
	// static headers; the method check is explicit rather than left to the
	// file server.
	mux.Handle("/static/", middleware.LogRoute("/static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static files bypass SecurityHeaders. An SVG opened directly is a
		// document on this origin, so it gets no script, no loads and a
		// sandbox; pages using a file as <img>, <use>, a script or a
		// stylesheet ignore a subresource's own policy.
		w.Header().Set("Content-Security-Policy", staticContentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
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
	}))))
	// LimitBody must see the server's real writer so oversized bodies close the
	// connection. It never answers itself, so RequestLog still logs every request.
	// CrossOriginProtection lets GET, HEAD and OPTIONS through, so every route
	// that changes state must be a POST.
	logger := services.RequestLogger
	if logger == nil {
		// Capture the process logger, configured before construction by run.
		logger = slog.Default()
	}
	return middleware.LimitBody(middleware.RequestLog(logger, http.NewCrossOriginProtection().Handler(mux))), *routes.patterns, nil
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
	session := middleware.Session(m.sessions, handler)
	m.ServeMux.Handle(pattern, middleware.LogRoute(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Include session and authorization work in typing acceptance deadlines.
		r = r.WithContext(context.WithValue(r.Context(), handlingStartKey{}, time.Now()))
		session.ServeHTTP(w, r)
	})))
}

// HandleFuncWithoutSession registers an HTML route that must decide whether
// it is open before any session lookup (setup, sign-up).
func (m sessionMux) HandleFuncWithoutSession(pattern string, f func(http.ResponseWriter, *http.Request)) {
	*m.patterns = append(*m.patterns, pattern)
	m.ServeMux.Handle(pattern, middleware.LogRoute(pattern, http.HandlerFunc(f)))
}

// HandleFunc registers f for pattern behind the session middleware.
func (m sessionMux) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(f))
}
