package i18n

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLanguageNegotiation(t *testing.T) {
	c, err := New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name       string
		middleware bool
		cookie     string
		acceptLang string
		presetVary []string
		wantLang   string
		wantT      string
		wantVary   string
	}{
		{name: "Language without middleware", cookie: "ja", acceptLang: "ja", wantLang: "en"},
		{name: "T without middleware", cookie: "ja", acceptLang: "ja", wantT: "hello.title"},
		{name: "Accept-Language en", middleware: true, acceptLang: "en", wantLang: "en", wantT: "Hello from ribbitto", wantVary: "Accept-Language|Cookie"},
		{name: "Accept-Language ja", middleware: true, acceptLang: "ja", wantLang: "ja", wantT: "ribbittoからこんにちは", wantVary: "Accept-Language|Cookie"},
		{name: "cookie ja overrides Accept-Language en", middleware: true, cookie: "ja", acceptLang: "en", wantLang: "ja", wantT: "ribbittoからこんにちは", wantVary: "Accept-Language|Cookie"},
		{name: "cookie en overrides Accept-Language ja", middleware: true, cookie: "en", acceptLang: "ja", wantLang: "en", wantT: "Hello from ribbitto", wantVary: "Accept-Language|Cookie"},
		{name: "invalid cookie falls back to Accept-Language", middleware: true, cookie: "fr", acceptLang: "ja", wantLang: "ja", wantT: "ribbittoからこんにちは", wantVary: "Accept-Language|Cookie"},
		{name: "garbage Accept-Language falls back to en", middleware: true, acceptLang: "ja;q=broken", wantLang: "en", wantT: "Hello from ribbitto", wantVary: "Accept-Language|Cookie"},
		{name: "Vary deduplicates case-insensitive comma list", middleware: true, presetVary: []string{"Accept-Encoding, accept-language , cookie"}, wantLang: "en", wantVary: "Accept-Encoding, accept-language , cookie"},
		{name: "Vary deduplicates existing accept-language", middleware: true, presetVary: []string{"accept-language"}, wantLang: "en", wantVary: "accept-language|Cookie"},
		{name: "Vary deduplicates existing cookie", middleware: true, presetVary: []string{"COOKIE"}, wantLang: "en", wantVary: "COOKIE|Accept-Language"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var handler http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				calls++
				if tt.wantLang != "" {
					if got := Language(r.Context()); got != tt.wantLang {
						t.Errorf("Language = %q, want %q", got, tt.wantLang)
					}
				}
				if tt.wantT != "" {
					if got := T(r.Context(), "hello.title"); got != tt.wantT {
						t.Errorf("T = %q, want %q", got, tt.wantT)
					}
				}
			})
			if tt.middleware {
				handler = c.Middleware(handler)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.acceptLang != "" {
				r.Header.Set("Accept-Language", tt.acceptLang)
			}
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "lang", Value: tt.cookie})
			}
			w := httptest.NewRecorder()
			for _, vary := range tt.presetVary {
				w.Header().Add("Vary", vary)
			}
			handler.ServeHTTP(w, r)
			if calls != 1 {
				t.Fatalf("handler calls = %d, want 1", calls)
			}
			if gotVary := strings.Join(w.Header().Values("Vary"), "|"); gotVary != tt.wantVary {
				t.Errorf("Vary = %q, want %q", gotVary, tt.wantVary)
			}
		})
	}
}
