package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'nonce-%s'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// SecurityHeaders supplies a fresh script nonce and security headers for HTML routes.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		nonce := base64.StdEncoding.EncodeToString(random[:])
		w.Header().Set("Content-Security-Policy", fmt.Sprintf(contentSecurityPolicy, nonce))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
	})
}
