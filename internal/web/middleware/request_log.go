package middleware

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"time"
)

type requestKey struct{}
type requestLog struct{ id, route string }

// RequestID returns the server-generated ID, or empty outside RequestLog.
func RequestID(ctx context.Context) string {
	if state, ok := ctx.Value(requestKey{}).(*requestLog); ok {
		return state.id
	}
	return ""
}

// LogRoute records a registration pattern, never a client-supplied path.
func LogRoute(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state, ok := r.Context().Value(requestKey{}).(*requestLog); ok {
			state.route = pattern
		}
		next.ServeHTTP(w, r)
	})
}

// RequestLog assigns an ID and logs only allow-listed fields when a request ends.
func RequestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		state := &requestLog{id: rand.Text(), route: "unmatched"}
		r = r.WithContext(context.WithValue(r.Context(), requestKey{}, state))
		response := &logResponse{ResponseWriter: w}
		completed := false
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
				if !completed {
					status = http.StatusInternalServerError
				}
			}
			logger.InfoContext(r.Context(), "request", "request_id", state.id,
				"method", r.Method, "route", state.route, "status", status,
				"duration", time.Since(started))
		}()
		next.ServeHTTP(response, r)
		completed = true
	})
}

// logResponse accepts losing io.ReaderFrom's sendfile optimization for small static files.
type logResponse struct {
	http.ResponseWriter
	status int
}

// Unwrap preserves ResponseController's access to stream deadlines and flushing.
func (w *logResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *logResponse) WriteHeader(status int) {
	if w.status == 0 && (status >= 100 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *logResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *logResponse) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
