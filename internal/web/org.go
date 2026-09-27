package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Authorizer decides organisation access (authz.Authorizer).
type Authorizer interface {
	Member(ctx context.Context, account *domain.Account, slug string) (authz.Membership, error)
	HomeSlug(ctx context.Context, account *domain.Account) (string, error)
}

// orgRoute is a page under /o/{slug}. Its handler receives the membership
// that authz resolved and never reads {slug} itself.
type orgRoute struct {
	method, path string // path below /o/{slug}
	handle       func(w http.ResponseWriter, r *http.Request, m authz.Membership)
}

// orgRoutes lists every organisation route. The same list registers the
// routes and drives the tests that prove non-members get 404, so a route
// cannot be added without those tests covering it.
func orgRoutes(pages *pageRenderer) []orgRoute {
	return []orgRoute{
		{http.MethodGet, "/{$}", func(w http.ResponseWriter, r *http.Request, m authz.Membership) {
			account, _ := middleware.Account(r.Context())
			pages.render(w, r, http.StatusOK, func(url string) templ.Component {
				return view.OrgHome(url, view.OrgPage{
					OrganizationName: m.Organization.Name,
					DisplayName:      account.DisplayName,
					Role:             string(m.Member.Role),
				})
			})
		}},
	}
}

// registerOrgRoutes puts every organisation route behind authz. A signed-out
// request gets 404 too, not a redirect to sign-in: a redirect would reveal
// which slugs exist.
func registerOrgRoutes(routes sessionMux, authorizer Authorizer, table []orgRoute) {
	for _, route := range table {
		routes.HandleFunc(route.method+" /o/{slug}"+route.path, func(w http.ResponseWriter, r *http.Request) {
			var account *domain.Account
			if a, ok := middleware.Account(r.Context()); ok {
				account = &a
			}
			membership, err := authorizer.Member(r.Context(), account, r.PathValue("slug"))
			if errors.Is(err, authz.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				serverError(w, r, "authorising organisation request", err)
				return
			}
			route.handle(w, r, membership)
		})
	}
}
