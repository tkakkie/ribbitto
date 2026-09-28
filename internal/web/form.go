package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// parseForm parses a POST form, answering 413 when the body is over the
// limit set by middleware.LimitBody and 400 when it is malformed.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	err := r.ParseForm()
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}
	return false
}

// serverError logs an unexpected error and answers 500 without details.
func serverError(w http.ResponseWriter, r *http.Request, doing string, err error) {
	slog.ErrorContext(r.Context(), doing, "err", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// incomingSession is the browser's session token, empty if it sent none.
// Every flow that issues a session passes it to Replace, so the new session
// ends the old one.
func incomingSession(r *http.Request) string {
	if cookie, err := r.Cookie(middleware.SessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}
