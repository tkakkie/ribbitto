package i18n

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

func TestCatalogueParity(t *testing.T) {
	c, err := New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	for _, lang := range []string{"en", "ja"} {
		file, err := c.bundle.LoadMessageFileFS(locales, "locales/"+lang+".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range file.Messages {
			ids[message.ID]++
			if strings.TrimSpace(message.Other) == "" {
				t.Errorf("%s: empty message %s", lang, message.ID)
			}
		}
	}
	for id, count := range ids {
		if count != 2 {
			t.Errorf("%s is not present in both catalogues", id)
		}
	}
}

func TestMessageFallback(t *testing.T) {
	var logs bytes.Buffer
	c, err := New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.bundle.AddMessages(language.English,
		&goi18n.Message{ID: "test.english", Other: "English fallback"},
		&goi18n.Message{ID: "test.empty", Other: "Empty translation fallback"}); err != nil {
		t.Fatal(err)
	}
	if err := c.bundle.AddMessages(language.Japanese, &goi18n.Message{ID: "test.empty"}); err != nil {
		t.Fatal(err)
	}
	// Pin the library's text-plus-error behavior so the wrapper cannot discard it.
	text, err := goi18n.NewLocalizer(c.bundle, "ja").Localize(&goi18n.LocalizeConfig{MessageID: "test.english"})
	if text != "English fallback" || err == nil {
		t.Fatalf("Localize = %q, %v; want English text and an error", text, err)
	}
	for _, tt := range []struct{ lang, id, want string }{
		{"ja", "test.english", "English fallback"},
		{"ja", "test.empty", "Empty translation fallback"},
		{"ja", "test.missing", "test.missing"},
		{"en", "test.missing", "test.missing"},
	} {
		t.Run(tt.lang+"/"+tt.id, func(t *testing.T) {
			logs.Reset()
			handler := c.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := T(r.Context(), tt.id); got != tt.want {
					t.Errorf("T = %q, want %q", got, tt.want)
				}
			}))
			for range 3 {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Accept-Language", tt.lang)
				handler.ServeHTTP(httptest.NewRecorder(), r)
			}
			if strings.Count(logs.String(), "message fallback") != 1 ||
				!strings.Contains(logs.String(), "language="+tt.lang) || !strings.Contains(logs.String(), "message_id="+tt.id) {
				t.Errorf("want one log for language/ID, got %q", logs.String())
			}
		})
	}
}
