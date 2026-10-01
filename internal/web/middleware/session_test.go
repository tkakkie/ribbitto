package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// cookieAttributes splits a Set-Cookie header into its value and attributes.
func cookieAttributes(t *testing.T, header string) (string, map[string]string) {
	t.Helper()
	parts := strings.Split(header, "; ")
	attributes := map[string]string{}
	for _, part := range parts[1:] {
		name, value, _ := strings.Cut(part, "=")
		attributes[name] = value
	}
	return parts[0], attributes
}

func TestSessionCookieAttributes(t *testing.T) {
	w := httptest.NewRecorder()
	middleware.SetSessionCookie(w, "token", time.Now().Add(time.Hour))
	middleware.ClearSessionCookie(w)
	headers := w.Header().Values("Set-Cookie")
	if len(headers) != 2 {
		t.Fatalf("Set-Cookie headers = %q", headers)
	}
	for i, want := range []struct {
		value  string
		maxAge func(int) bool
	}{
		{"__Host-session=token", func(s int) bool { return s > 3590 && s <= 3600 }},
		{"__Host-session=", func(s int) bool { return s == 0 }},
	} {
		value, attributes := cookieAttributes(t, headers[i])
		if value != want.value {
			t.Errorf("cookie %d = %q, want %q", i, value, want.value)
		}
		seconds, err := strconv.Atoi(attributes["Max-Age"])
		if err != nil || !want.maxAge(seconds) {
			t.Errorf("cookie %d: Max-Age = %q", i, attributes["Max-Age"])
		}
		for name, value := range map[string]string{"Path": "/", "HttpOnly": "", "Secure": "", "SameSite": "Lax"} {
			if got, ok := attributes[name]; !ok || got != value {
				t.Errorf("cookie %d: %s = %q, %v; want %q", i, name, got, ok, value)
			}
		}
		if _, ok := attributes["Domain"]; ok {
			t.Errorf("cookie %d has a Domain attribute", i)
		}
	}
}

func TestSessionCookieMaxAgeBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		until  time.Duration
		maxAge string
	}{
		{"half a second left", 500 * time.Millisecond, "1"},
		{"already expired", -time.Second, "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			middleware.SetSessionCookie(w, "token", time.Now().Add(tt.until))
			_, attributes := cookieAttributes(t, w.Header().Get("Set-Cookie"))
			if got, ok := attributes["Max-Age"]; !ok || got != tt.maxAge {
				t.Fatalf("Max-Age = %q, %v; want %q", got, ok, tt.maxAge)
			}
		})
	}
}

// fakeResolver answers by token; every token it does not know is signed out.
type fakeResolver map[string]error

var alice = domain.Account{ID: domain.ID{1}, Email: "alice@example.com", DisplayName: "Alice"}

// aliceSession is the session fakeResolver signs Alice in with.
var aliceSession = auth.Session{ID: domain.ID{9}, ExpiresAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}

func (f fakeResolver) Resolve(_ context.Context, token string) (domain.Account, auth.Session, error) {
	err, ok := f[token]
	if !ok {
		return domain.Account{}, auth.Session{}, auth.ErrNoSession
	}
	if err != nil {
		return domain.Account{}, auth.Session{}, err
	}
	return alice, aliceSession, nil
}

func TestSession(t *testing.T) {
	resolver := fakeResolver{
		"live":   nil,
		"outage": fmt.Errorf("resolving session: %w", errors.New("connection refused")),
	}
	for _, tt := range []struct {
		name        string
		cookie      string // empty: no cookie
		status      int
		signedIn    bool
		cleared     bool
		noStore     bool
		reachedNext bool
	}{
		{"no cookie", "", http.StatusOK, false, false, false, true},
		{"live session", "live", http.StatusOK, true, false, true, true},
		// Malformed, unknown, deleted and expired tokens all come back from
		// auth.Sessions as ErrNoSession (tested there); here they are tokens
		// the fake does not know.
		{"malformed", "not base64!", http.StatusOK, false, true, false, true},
		{"unknown, deleted or expired", "gone", http.StatusOK, false, true, false, true},
		{"store error", "outage", http.StatusInternalServerError, false, false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				account, ok := middleware.Account(r.Context())
				if ok != tt.signedIn || (ok && account != alice) {
					t.Errorf("Account = %+v, %v; want signed in %v", account, ok, tt.signedIn)
				}
			})
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: tt.cookie})
			}
			w := httptest.NewRecorder()
			middleware.Session(resolver, next).ServeHTTP(w, r)
			if w.Code != tt.status || reached != tt.reachedNext {
				t.Fatalf("status %d, next reached %v; want %d, %v", w.Code, reached, tt.status, tt.reachedNext)
			}
			setCookie := w.Header().Get("Set-Cookie")
			if cleared := strings.HasPrefix(setCookie, "__Host-session=;") && strings.Contains(setCookie, "Max-Age=0"); cleared != tt.cleared || (!tt.cleared && setCookie != "") {
				t.Errorf("Set-Cookie = %q, want cleared %v", setCookie, tt.cleared)
			}
			if noStore := w.Header().Get("Cache-Control") == "no-store"; noStore != tt.noStore {
				t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestLimitBody(t *testing.T) {
	// Like a form handler: parse, and answer 413 when the body is too large.
	reads := middleware.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Bad Request", http.StatusBadRequest)
		}
	}))
	closed := middleware.LimitBody(http.NotFoundHandler())
	for _, tt := range []struct {
		name    string
		handler http.Handler
		size    int
		status  int
	}{
		{"at the limit", reads, middleware.MaxBodyBytes, http.StatusOK},
		{"over the limit", reads, middleware.MaxBodyBytes + 1, http.StatusRequestEntityTooLarge},
		{"closed route ignores the body", closed, 2 * middleware.MaxBodyBytes, http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := "a=" + strings.Repeat("x", tt.size-2)
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			tt.handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
		})
	}
}
