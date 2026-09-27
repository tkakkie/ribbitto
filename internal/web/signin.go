package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// SignInService signs accounts in and out (auth.SignIn).
type SignInService interface {
	SignIn(ctx context.Context, email, password, previousToken string) (string, time.Time, error)
	SignOut(ctx context.Context, token string) error
}

func registerSignIn(routes sessionMux, pages *pageRenderer, service SignInService, signup SignUpService, allow func(http.ResponseWriter, *http.Request) bool) {
	render := func(w http.ResponseWriter, r *http.Request, status int, form view.SignInForm) {
		if signup != nil {
			open, err := signup.Open(r.Context())
			if err != nil {
				serverError(w, r, "checking sign-up", err)
				return
			}
			form.SignUpOpen = open
		}
		pages.render(w, r, status, func(url string) templ.Component { return view.SignIn(url, form) })
	}
	routes.HandleFunc("GET /signin", func(w http.ResponseWriter, r *http.Request) {
		if _, signedIn := middleware.Account(r.Context()); signedIn {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		render(w, r, http.StatusOK, view.SignInForm{})
	})
	routes.HandleFunc("POST /signin", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r) || !parseForm(w, r) {
			return
		}
		email := r.PostForm.Get("email")
		previous := ""
		if cookie, err := r.Cookie(middleware.SessionCookie); err == nil {
			previous = cookie.Value
		}
		token, expiresAt, err := service.SignIn(r.Context(), email, r.PostForm.Get("password"), previous)
		var message string
		switch {
		case err == nil:
			middleware.SetSessionCookie(w, token, expiresAt)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		case errors.Is(err, auth.ErrInvalidInput):
			message = "signin.error.required"
		case errors.Is(err, auth.ErrInvalidCredentials):
			// One message for an unknown email and a wrong password.
			message = "signin.error.invalid"
		case errors.Is(err, auth.ErrBusy):
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		default:
			serverError(w, r, "signing in", err)
			return
		}
		// Keep the typed email; never echo the password.
		render(w, r, http.StatusUnprocessableEntity, view.SignInForm{Email: email, Error: message})
	})
	routes.HandleFunc("POST /signout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(middleware.SessionCookie); err == nil {
			if err := service.SignOut(r.Context(), cookie.Value); err != nil {
				serverError(w, r, "signing out", err)
				return
			}
		}
		middleware.ClearSessionCookie(w)
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
	})
}
