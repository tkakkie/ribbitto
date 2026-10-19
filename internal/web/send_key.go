package web

import (
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func sendKey(preferences *identity.Preferences, limit *middleware.MemberRateLimiter) func(http.ResponseWriter, *http.Request, org.Membership) {
	return func(w http.ResponseWriter, r *http.Request, m org.Membership) {
		account, signedIn := middleware.Account(r.Context())
		if !signedIn {
			http.NotFound(w, r)
			return
		}
		if !limit.Allow(m.Organization.ID, m.Member.ID) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if !parseForm(w, r) {
			return
		}
		err := preferences.SetSendKey(r.Context(), account.ID, r.PostForm.Get("send_key"))
		switch {
		case errors.Is(err, identity.ErrInvalidSendKey):
			http.Error(w, "Bad Request", http.StatusBadRequest)
		case errors.Is(err, identity.ErrNoAccount):
			http.NotFound(w, r)
		case err != nil:
			serverError(w, r, "changing send key", err)
		default:
			target := "/organizations/" + m.Organization.Slug + "/channels"
			if channel, ok := pathID(r.PostForm.Get("return_channel")); ok {
				var topic *kernel.ID
				if selected, ok := pathID(r.PostForm.Get("return_topic")); ok {
					topic = &selected
				}
				target = view.ConversationURL(m.Organization.Slug, channel, topic)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
		}
	}
}
