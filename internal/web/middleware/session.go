package middleware

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// SessionCookie is the session cookie's name. The __Host- prefix makes the
// browser refuse it unless it is Secure, has Path=/ and has no Domain, so a
// sibling subdomain cannot set or overwrite it.
const SessionCookie = "__Host-session"

// SessionResolver turns a session token into its account. It returns
// auth.ErrNoSession for every token that does not sign anyone in.
type SessionResolver interface {
	Resolve(ctx context.Context, token string) (domain.Account, error)
}

type accountKey struct{}

// SetSessionCookie stores a session token in the browser until expiresAt.
func SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	// Round up: MaxAge 0 would mean "no Max-Age", a cookie that outlives the
	// session until the browser closes. An expiry already past deletes it.
	maxAge := int(math.Ceil(time.Until(expiresAt).Seconds()))
	if maxAge <= 0 {
		maxAge = -1
	}
	http.SetCookie(w, sessionCookie(token, maxAge))
}

// ClearSessionCookie tells the browser to drop the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	// MaxAge -1 is what net/http serialises as Max-Age=0.
	http.SetCookie(w, sessionCookie("", -1))
}

func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// Session resolves the session cookie and puts the signed-in account, if
// any, into the request context.
func Session(sessions SessionResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		account, err := sessions.Resolve(r.Context(), cookie.Value)
		switch {
		case errors.Is(err, auth.ErrNoSession):
			ClearSessionCookie(w)
			next.ServeHTTP(w, r)
		case err != nil:
			// Keep the cookie: a database outage must not sign everyone out.
			slog.ErrorContext(r.Context(), "resolving session", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		default:
			// Pages for a signed-in account must not be stored by browsers or
			// shared caches.
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey{}, account)))
		}
	})
}

// Account returns the signed-in account of the request, if there is one.
func Account(ctx context.Context) (domain.Account, bool) {
	account, ok := ctx.Value(accountKey{}).(domain.Account)
	return account, ok
}

// MaxBodyBytes bounds every request body. It is far above any form ribbitto
// has. The reader it installs cannot be undone downstream, so a route that
// needs more (uploads) will need LimitBody to exempt it or become
// route-aware; a handler cannot raise the limit by itself.
const MaxBodyBytes = 64 << 10

// LimitBody caps request bodies at MaxBodyBytes. Reading past the cap fails
// with *http.MaxBytesError, which handlers answer with 413. A handler that
// never reads the body (a closed route answering 404) is unaffected.
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
