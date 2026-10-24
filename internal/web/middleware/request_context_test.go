package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestPeerAddrForms(t *testing.T) {
	for _, tt := range []struct {
		name   string
		remote string
		want   string
	}{
		{"IPv4 without port", "198.51.100.7", "198.51.100.7"},
		{"IPv6 without port", "2001:db8::1", "2001:db8::1"},
		{"IPv4-mapped IPv6 without port", "::ffff:198.51.100.7", "198.51.100.7"},
		{"IPv4-mapped IPv6 with port", "[::ffff:198.51.100.7]:1234", "198.51.100.7"},
		{"IPv6 zone dropped", "[fe80::1%eth0]:1234", "fe80::1"},
		{"hostname with port", "example.com:80", "::"},
		{"empty remote address", "", "::"},
		{"unparseable remote address", "not-an-address", "::"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			if got, want := peerAddr(r), netip.MustParseAddr(tt.want); got != want {
				t.Fatalf("peerAddr(%q) = %q, want %q", tt.remote, got, want)
			}
		})
	}

	const (
		trustedProxyCIDR = "10.0.0.0/8"
		forwardedClient  = "203.0.113.9"
	)
	trusted, err := ParseTrustedProxies(trustedProxyCIDR)
	if err != nil {
		t.Fatal(err)
	}
	// Unparseable peers share "::/64" and are not trusted proxies, so X-Forwarded-For is ignored.
	sharedUnspecifiedKey := netip.MustParsePrefix("::/64")
	for _, tt := range []struct {
		name   string
		remote string
	}{
		{"empty peer", ""},
		{"unparseable peer", "garbage"},
		{"hostname peer", "example.com:80"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/signin", nil)
			r.RemoteAddr = tt.remote
			r.Header.Set("X-Forwarded-For", forwardedClient)
			if got := ClientKey(r, trusted); got != sharedUnspecifiedKey {
				t.Fatalf("ClientKey(%q) = %s, want %s", tt.remote, got, sharedUnspecifiedKey)
			}
		})
	}
}

func TestRequestContextOutsideRequestLog(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("RequestID(context.Background()) = %q, want empty", got)
	}
	const (
		registeredPattern = "GET /x/{id}"
		requestPath       = "/x/1"
	)
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := RequestID(r.Context()); got != "" {
			t.Errorf("RequestID(r.Context()) = %q, want empty", got)
		}
		w.WriteHeader(http.StatusTeapot)
	})
	w := httptest.NewRecorder()
	LogRoute(registeredPattern, next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, requestPath, nil))
	if calls != 1 {
		t.Fatalf("next calls = %d, want 1", calls)
	}
	if w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTeapot)
	}
}

func TestRequestLogRecordedStatus(t *testing.T) {
	for _, tt := range []struct {
		name     string
		statuses []int
		want     int
	}{
		{"writes nothing", nil, http.StatusOK},
		{"switching protocols is final", []int{http.StatusSwitchingProtocols, http.StatusInternalServerError}, http.StatusSwitchingProtocols},
		{"continue before final status", []int{http.StatusContinue, http.StatusNoContent}, http.StatusNoContent},
		{"early hints only", []int{http.StatusEarlyHints}, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := RequestLog(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, status := range tt.statuses {
					w.WriteHeader(status)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
			if strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("want one log line, got %s", logs.String())
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["route"] != "unmatched" {
				t.Fatalf("route = %v, want unmatched", record["route"])
			}
			if record["status"] != float64(tt.want) {
				t.Fatalf("status = %v, want %d", record["status"], tt.want)
			}
		})
	}
}

func TestLogResponseWriteAfterHeader(t *testing.T) {
	underlying := &headerRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := &logResponse{ResponseWriter: underlying}
	w.WriteHeader(http.StatusCreated)
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatal(err)
	}
	if len(underlying.statuses) != 1 || underlying.statuses[0] != http.StatusCreated {
		t.Fatalf("statuses = %v, want [%d]", underlying.statuses, http.StatusCreated)
	}
	if w.status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.status, http.StatusCreated)
	}
}

type currentSessionResolver struct {
	token   string
	account identity.Account
	session identity.Session
}

func (r currentSessionResolver) Resolve(_ context.Context, token string) (identity.Account, identity.Session, error) {
	if token != r.token {
		return identity.Account{}, identity.Session{}, identity.ErrNoSession
	}
	return r.account, r.session, nil
}

func TestCurrentSession(t *testing.T) {
	if session, ok := CurrentSession(context.Background()); ok || session != (identity.Session{}) {
		t.Fatalf("CurrentSession(context.Background()) = %+v, %v; want zero, false", session, ok)
	}
	const (
		liveToken    = "live"
		unknownToken = "gone"
	)
	accountID := kernel.ID{1}
	sessionID := kernel.ID{9}
	wantAccount := identity.Account{ID: accountID}
	wantSession := identity.Session{
		ID:        sessionID,
		ExpiresAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
	}
	resolver := currentSessionResolver{
		token:   liveToken,
		account: wantAccount,
		session: wantSession,
	}
	for _, tt := range []struct {
		name         string
		cookie       string
		wantSignedIn bool
	}{
		{"no cookie", "", false},
		{"unknown token", unknownToken, false},
		{"live token", liveToken, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				calls++
				session, sessionOK := CurrentSession(r.Context())
				account, accountOK := Account(r.Context())
				if !tt.wantSignedIn {
					if sessionOK || session != (identity.Session{}) {
						t.Errorf("CurrentSession = %+v, %v; want zero, false", session, sessionOK)
					}
					if accountOK || account != (identity.Account{}) {
						t.Errorf("Account = %+v, %v; want zero, false", account, accountOK)
					}
					return
				}
				if !sessionOK || session != wantSession {
					t.Errorf("CurrentSession = %+v, %v; want %+v, true", session, sessionOK, wantSession)
				}
				if !accountOK || account.ID != wantAccount.ID {
					t.Errorf("Account = %+v, %v; want ID %v, true", account, accountOK, wantAccount.ID)
				}
			})
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tt.cookie})
			}
			Session(resolver, next).ServeHTTP(httptest.NewRecorder(), r)
			if calls != 1 {
				t.Fatalf("next calls = %d, want 1", calls)
			}
		})
	}
}
