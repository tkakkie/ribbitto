package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// SetupService checks availability and creates the organisation and owner.
type SetupService interface {
	Open(context.Context) (bool, error)
	Complete(context.Context, string, setup.Input) (setup.Result, error)
}

// SessionReplacer signs a newly created account in. Like sign-in, it ends
// the browser's previous session (the incoming cookie, possibly empty or
// stale) in the same transaction, so every flow that issues a session
// replaces the one before it. An uncertain outcome ends its streams. It
// offers no plain Create, so a new flow cannot forget that.
type SessionReplacer interface {
	Replace(ctx context.Context, previousToken string, accountID domain.ID) (string, time.Time, error)
}

func registerSetup(routes sessionMux, pages *pageRenderer, service SetupService, sessions SessionReplacer, allow func(http.ResponseWriter, *http.Request) bool) {
	// Without a setup token the routes do not exist: /setup is then an
	// unknown path, answered 404 by the router without a session lookup.
	if service == nil {
		return
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		open, err := service.Open(r.Context())
		if errors.Is(err, setup.ErrCompleted) || (err == nil && !open) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			serverError(w, r, "checking setup", err)
			return
		}
		form := view.SetupForm{Values: map[string]string{}, Errors: map[string]string{}}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			if !allow(w, r) || !parseForm(w, r) {
				return
			}
			for _, name := range []string{"organization_name", "slug", "display_name", "handle", "email"} {
				form.Values[name] = r.PostForm.Get(name)
			}
			result, err := service.Complete(r.Context(), r.PostForm.Get("token"), setup.Input{
				OrganizationName: form.Values["organization_name"], Slug: form.Values["slug"],
				DisplayName: form.Values["display_name"], Handle: form.Values["handle"], Email: form.Values["email"], Password: r.PostForm.Get("password"),
			})
			var fields setup.ValidationErrors
			switch {
			case err == nil:
				token, expiresAt, err := sessions.Replace(r.Context(), incomingSession(r), result.AccountID)
				if err != nil {
					serverError(w, r, "creating setup session", err)
					return
				}
				middleware.SetSessionCookie(w, token, expiresAt)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			case errors.Is(err, setup.ErrCompleted):
				http.NotFound(w, r)
				return
			case errors.Is(err, identity.ErrBusy):
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				return
			case errors.Is(err, setup.ErrToken):
				form.Errors["token"] = "setup.error.token"
			case errors.As(err, &fields):
				for _, name := range []string{"organization_name", "slug", "display_name", "handle", "email", "password"} {
					if _, invalid := fields[name]; invalid {
						form.Errors[name] = "setup.error." + name
					}
				}
			default:
				serverError(w, r, "completing setup", err)
				return
			}
			status = http.StatusUnprocessableEntity
		}
		pages.render(w, r, status, func(url string) templ.Component { return view.Setup(url, form) })
	}
	// Registered on the plain mux, not behind the session middleware: setup
	// needs no signed-in account, so whether setup is open is decided first
	// and a completed setup answers 404 whatever the session state.
	routes.HandleFuncWithoutSession("GET /setup", handler)
	routes.HandleFuncWithoutSession("POST /setup", handler)
}
