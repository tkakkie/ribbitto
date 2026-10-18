package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Pinger checks database connectivity without reading organisation data.
type Pinger interface {
	Ping(context.Context) error
}

const readinessTimeout = time.Second

func readiness(database Pinger, shuttingDown func() bool) http.HandlerFunc {
	stopping := func() bool { return shuttingDown != nil && shuttingDown() }
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		unavailable := func() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable\n"))
		}
		if database == nil || stopping() {
			unavailable()
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if err := database.Ping(ctx); err != nil || ctx.Err() != nil {
			// Driver errors may include connection credentials; log only the failure.
			slog.ErrorContext(r.Context(), "readiness database ping failed")
			unavailable()
			return
		}
		// Shutdown can begin while the ping is in flight.
		if stopping() {
			unavailable()
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	}
}
