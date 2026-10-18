package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func TestSecurityHeaders(t *testing.T) {
	for _, tt := range []struct{ header, want string }{
		{"Cross-Origin-Opener-Policy", "same-origin"},
		{"Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()"},
	} {
		t.Run(tt.header, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler := middleware.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := w.Result().Header.Get(tt.header); got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}
