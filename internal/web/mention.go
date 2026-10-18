package web

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// MemberSearch finds candidates using the session and URL's resolved membership.
type MemberSearch interface {
	Search(context.Context, org.Membership, string) ([]org.MemberCandidate, error)
}

func mentionCandidates(search MemberSearch, limit *middleware.MemberRateLimiter) func(http.ResponseWriter, *http.Request, org.Membership) {
	return func(w http.ResponseWriter, r *http.Request, m org.Membership) {
		w.Header().Set("Cache-Control", "no-store")
		if search == nil {
			http.NotFound(w, r)
			return
		}
		release, admitted := limit.Admit(m.Organization.ID, m.Member.ID)
		if !admitted {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		defer release()
		query := r.URL.Query().Get("q")
		candidates, err := search.Search(r.Context(), m, query)
		if err != nil {
			serverError(w, r, "searching mention candidates", err)
			return
		}
		members := make([]view.Member, len(candidates))
		for i, candidate := range candidates {
			members[i] = view.Member{ID: candidate.ID, Handle: candidate.Handle, DisplayName: candidate.DisplayName}
		}
		templ.Handler(view.MentionCandidates(query, members)).ServeHTTP(w, r)
	}
}
