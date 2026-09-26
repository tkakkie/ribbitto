package i18n

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml
var locales embed.FS

// Catalogues holds immutable messages and deduplicates fallback logs. Construct
// one at process startup and share it across all HTML handlers.
type Catalogues struct {
	bundle  *goi18n.Bundle
	matcher language.Matcher
	logger  *slog.Logger
	logged  sync.Map
}

// New loads the embedded English and Japanese catalogues.
func New(logger *slog.Logger) (*Catalogues, error) {
	bundle := goi18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, lang := range []string{"en", "ja"} {
		if _, err := bundle.LoadMessageFileFS(locales, "locales/"+lang+".toml"); err != nil {
			return nil, fmt.Errorf("loading %s catalogue: %w", lang, err)
		}
	}
	return &Catalogues{bundle: bundle, logger: logger,
		matcher: language.NewMatcher([]language.Tag{language.English, language.Japanese})}, nil
}

type contextKey struct{}
type requestLanguage struct {
	catalogues *Catalogues
	localizer  *goi18n.Localizer
	lang       string
}

// Middleware supplies translations and cache variation for HTML routes.
func (c *Catalogues) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := "en"
		if cookie, err := r.Cookie("lang"); err == nil && (cookie.Value == "en" || cookie.Value == "ja") {
			lang = cookie.Value
		} else if tags, _, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language")); err == nil {
			_, index, _ := c.matcher.Match(tags...)
			lang = []string{"en", "ja"}[index]
		}
		for _, token := range []string{"Accept-Language", "Cookie"} {
			found := false
			for _, value := range w.Header().Values("Vary") {
				for _, existing := range strings.Split(value, ",") {
					found = found || strings.EqualFold(strings.TrimSpace(existing), token)
				}
			}
			if !found {
				w.Header().Add("Vary", token)
			}
		}
		state := requestLanguage{c, goi18n.NewLocalizer(c.bundle, lang), lang}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, state)))
	})
}

// Language returns the negotiated catalogue language for the HTML lang attribute.
func Language(ctx context.Context) string {
	if state, ok := ctx.Value(contextKey{}).(requestLanguage); ok {
		return state.lang
	}
	return "en"
}

// T translates an ID, falling back to English and finally the ID itself.
func T(ctx context.Context, id string) string {
	state, ok := ctx.Value(contextKey{}).(requestLanguage)
	if !ok {
		return id
	}
	config := &goi18n.LocalizeConfig{MessageID: id}
	text, err := state.localizer.Localize(config)
	if err == nil && text != "" {
		return text
	}
	if _, loaded := state.catalogues.logged.LoadOrStore([2]string{state.lang, id}, true); !loaded {
		state.catalogues.logger.WarnContext(ctx, "message fallback", "language", state.lang, "message_id", id, "err", err)
	}
	// go-i18n can supply usable English text alongside a missing-translation error.
	if text != "" {
		return text
	}
	text, _ = goi18n.NewLocalizer(state.catalogues.bundle, "en").Localize(config)
	if text != "" {
		return text
	}
	return id
}
