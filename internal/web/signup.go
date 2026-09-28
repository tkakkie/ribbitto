package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// SignUpService exposes registration availability to installation-wide pages.
type SignUpService interface {
	Open(context.Context) (bool, error)
	SignUp(context.Context, string, string, string) (domain.ID, error)
}

func registerSignUp(routes sessionMux, pages *pageRenderer, service SignUpService, sessions SessionReplacer, allow func(http.ResponseWriter, *http.Request) bool) {
	// As with setup: without a sign-up service the routes do not exist, and
	// with one they sit outside the session middleware, so whether sign-up
	// is open is decided first, whatever the session state.
	if service == nil {
		return
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		open, err := service.Open(r.Context())
		if errors.Is(err, signup.ErrClosed) || (err == nil && !open) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			serverError(w, r, "checking sign-up", err)
			return
		}
		form := view.SignUpForm{Values: map[string]string{}, Errors: map[string]string{}}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			if !allow(w, r) || !parseForm(w, r) {
				return
			}
			for _, name := range []string{"display_name", "email"} {
				form.Values[name] = r.PostForm.Get(name)
			}
			account, err := service.SignUp(r.Context(), form.Values["display_name"], form.Values["email"], r.PostForm.Get("password"))
			var fields signup.ValidationErrors
			switch {
			case err == nil:
				token, expiresAt, err := sessions.Replace(r.Context(), incomingSession(r), account)
				if err != nil {
					serverError(w, r, "creating sign-up session", err)
					return
				}
				middleware.SetSessionCookie(w, token, expiresAt)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			case errors.Is(err, signup.ErrClosed):
				http.NotFound(w, r)
				return
			case errors.Is(err, auth.ErrBusy):
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				return
			case errors.Is(err, signup.ErrEmailTaken):
				form.Errors["email"] = "signup.error.email_taken"
			case errors.As(err, &fields):
				for _, name := range []string{"display_name", "email", "password"} {
					if _, invalid := fields[name]; invalid {
						form.Errors[name] = "setup.error." + name
					}
				}
			default:
				serverError(w, r, "signing up", err)
				return
			}
			status = http.StatusUnprocessableEntity
		}
		pages.render(w, r, status, func(url string) templ.Component { return view.SignUp(url, form) })
	}
	routes.ServeMux.HandleFunc("GET /signup", handler)
	routes.ServeMux.HandleFunc("POST /signup", handler)
}
