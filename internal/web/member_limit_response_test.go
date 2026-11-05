package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func TestMemberLimitFormResponse(t *testing.T) {
	cat, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, mode, accept, contentType, hx, method string
		html                                        bool
	}{
		{"browser", "navigate", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "application/x-www-form-urlencoded", "", "POST", true},
		{"multipart", "navigate", "text/html", "multipart/form-data; boundary=test", "", "POST", true},
		{"htmx", "navigate", "text/html", "application/x-www-form-urlencoded", "true", "POST", false},
		{"fetch HTML", "cors", "text/html", "application/x-www-form-urlencoded", "", "POST", false},
		{"fetch default", "cors", "*/*", "application/x-www-form-urlencoded", "", "POST", false},
		{"missing metadata", "", "text/html", "application/x-www-form-urlencoded", "", "POST", false},
		{"wildcard", "navigate", "*/*", "application/x-www-form-urlencoded", "", "POST", false},
		{"HTML excluded", "navigate", "text/html;q=0,*/*", "application/x-www-form-urlencoded", "", "POST", false},
		{"invalid quality", "navigate", "text/html;q=NaN", "application/x-www-form-urlencoded", "", "POST", false},
		{"exponent quality", "navigate", "text/html;q=1e-1", "application/x-www-form-urlencoded", "", "POST", false},
		{"leading zero quality", "navigate", "text/html;q=01", "application/x-www-form-urlencoded", "", "POST", false},
		{"excess precision quality", "navigate", "text/html;q=0.1234", "application/x-www-form-urlencoded", "", "POST", false},
		{"signed quality", "navigate", "text/html;q=+0.5", "application/x-www-form-urlencoded", "", "POST", false},
		{"invalid other quality", "navigate", "text/html,application/xml;q=1e-1", "application/x-www-form-urlencoded", "", "POST", false},
		{"zero decimal quality", "navigate", "text/html;q=0.", "application/x-www-form-urlencoded", "", "POST", false},
		{"one quality", "navigate", "text/html;q=1", "application/x-www-form-urlencoded", "", "POST", true},
		{"one decimal quality", "navigate", "text/html;q=1.", "application/x-www-form-urlencoded", "", "POST", true},
		{"one precision quality", "navigate", "text/html;q=1.000", "application/x-www-form-urlencoded", "", "POST", true},
		{"fraction quality", "navigate", "text/html;q=0.123", "application/x-www-form-urlencoded", "", "POST", true},
		{"invalid accept", "navigate", "text/html,invalid", "application/x-www-form-urlencoded", "", "POST", false},
		{"JSON", "navigate", "text/html", "application/json", "", "POST", false},
		{"GET", "navigate", "text/html", "application/x-www-form-urlencoded", "", "GET", false},
	} {
		for _, lang := range []string{"en", "ja"} {
			t.Run(tt.name+"/"+lang, func(t *testing.T) {
				now := time.Unix(1, 0)
				limit := middleware.NewMemberRateLimiterWithBudget(func() time.Time { return now }, 0.4, 1, 0)
				limit.Allow(kernel.ID{}, kernel.ID{})
				// Nil channels prove refusal never reaches channel creation or its writes.
				p := channelPages{createLimit: limit, pages: &pageRenderer{stylesheetURL: func() (string, error) { return "", nil }}}
				body := &readRecorder{}
				r := httptest.NewRequest(tt.method, "/organizations/acme/channels", body)
				r.Header.Set("Sec-Fetch-Mode", tt.mode)
				r.Header.Set("Accept", tt.accept)
				r.Header.Set("Content-Type", tt.contentType)
				r.Header.Set("Accept-Language", lang)
				if tt.hx != "" {
					r.Header.Set("HX-Request", tt.hx)
				}
				w := httptest.NewRecorder()
				cat.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					p.create(w, r, org.Membership{})
				})).ServeHTTP(w, r)
				if w.Code != 429 || w.Header().Get("Retry-After") != "3" || body.reads != 0 {
					t.Fatalf("status=%d retry=%q body reads=%d", w.Code, w.Header().Get("Retry-After"), body.reads)
				}
				if tt.html {
					title := "Too many attempts"
					if lang == "ja" {
						title = "試行回数が多すぎます"
					}
					if !strings.Contains(w.Body.String(), title) || !strings.Contains(w.Body.String(), `<html lang="`+lang+`"`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
						t.Fatalf("missing localized page: %s", w.Body.String())
					}
				} else if w.Body.Len() != 0 {
					t.Fatalf("unexpected body: %s", w.Body.String())
				}
				now = now.Add(2500 * time.Millisecond)
				if !limit.Allow(kernel.ID{}, kernel.ID{}) || limit.Allow(kernel.ID{}, kernel.ID{}) {
					t.Fatal("refusal consumed extra allowance")
				}
			})
		}
	}
}

func TestMemberLimitFractionalInterval(t *testing.T) {
	limit := middleware.NewMemberRateLimiterWithBudget(time.Now, 0.9999999995, 1, 0)
	w := httptest.NewRecorder()
	rejectMemberLimit(w, httptest.NewRequest(http.MethodPost, "/", nil), limit, nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" {
		t.Fatalf("status=%d retry=%q, want 429 and 2", w.Code, w.Header().Get("Retry-After"))
	}
}
