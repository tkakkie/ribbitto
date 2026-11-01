package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// Authorizer decides organisation access (org.Authorizer).
type Authorizer interface {
	Member(ctx context.Context, account *identity.Account, slug string) (org.Membership, error)
	HomeSlug(ctx context.Context, account *identity.Account) (string, error)
}

// orgRoute is a page under /organizations/{slug}/. Its handler receives
// the membership that org.Authorizer resolved and never reads {slug} itself.
type orgRoute struct {
	method, path string // path below /organizations/{slug}
	handle       func(w http.ResponseWriter, r *http.Request, m org.Membership)
}

// orgRoutes lists every route under /organizations/{slug}/. The same list
// registers the routes and drives the tests that prove non-members get
// 404, so a route cannot be added without those tests covering it.
func orgRoutes(pages *pageRenderer, services Services) []orgRoute {
	handlers := channelPages{typing: services.Typing, limiter: services.MemberLimits.Signal(), readLimit: services.MemberLimits.Read(), createLimit: services.MemberLimits.Create(), imageLimit: services.MemberLimits.Image(), branchLimit: services.MemberLimits.Branch(), postLimit: services.MemberLimits.Post(), pinLimit: services.MemberLimits.Pin(), reading: services.Reading, pages: pages, channels: services.Channels, topics: services.Topics, messages: services.Messages, posting: services.Posting, branching: services.Branching, pinning: services.Pinning, stream: services.Stream}
	if services.Stream != nil {
		handlers.renders = newRenderCache(services.Stream.Lifetime)
	}
	return []orgRoute{
		{http.MethodGet, "/{$}", handlers.home},
		{http.MethodPost, "/send-key", sendKey(services.Preferences, services.MemberLimits.SendKey())},
		{http.MethodGet, "/events", handlers.events},
		{http.MethodGet, "/mention-candidates", mentionCandidates(services.MemberSearch, services.MemberLimits.Mention())},
		{http.MethodPost, "/sidebar/resync", resyncSidebar(services.SidebarResync, services.MemberLimits.Resync())},
		{http.MethodPost, "/channels/{channelID}/topics/{topicID}/typing", handlers.signal},
		{http.MethodGet, "/channels/{channelID}", handlers.show},
		{http.MethodGet, "/channels/{channelID}/members", handlers.members},
		{http.MethodGet, "/channels/{channelID}/overview", handlers.overview},
		{http.MethodPost, "/channels/{channelID}/overview", handlers.changeImage},
		{http.MethodGet, "/channels/{channelID}/topics/{topicID}", handlers.show},
		{http.MethodGet, "/channels/{channelID}/topics/{topicID}/members", handlers.members},
		{http.MethodGet, "/channels/{channelID}/topics/{topicID}/overview", handlers.overview},
		{http.MethodPost, "/channels/{channelID}/topics/{topicID}/overview", handlers.changeImage},
		{http.MethodPost, "/channels/{channelID}/topics/{topicID}", handlers.show},
		{http.MethodPost, "/channels/{channelID}", handlers.show},
		{http.MethodPost, "/channels/{channelID}/branch", handlers.branch},
		{http.MethodPost, "/channels/{channelID}/quote", handlers.quote},
		{http.MethodPost, "/channels/{channelID}/messages/{messageID}/pin", handlers.pin},
		{http.MethodPost, "/channels/{channelID}/messages/{messageID}/unpin", handlers.unpin},
		{http.MethodPost, "/channels/{channelID}/read", handlers.read},
		{http.MethodPost, "/channels/{channelID}/topics/{topicID}/read", handlers.read},
		{http.MethodGet, "/messages/{messageID}", handlers.message},
		{http.MethodGet, "/channels", handlers.list},
		{http.MethodPost, "/channels", handlers.create},
	}
}

// registerOrgRoutes puts every route under /organizations/{slug}/ behind
// the Authorizer. A signed-out request gets 404 too, not a redirect to sign-in:
// a redirect would reveal which slugs exist.
func registerOrgRoutes(routes sessionMux, authorizer Authorizer, table []orgRoute) {
	for _, route := range table {
		routes.HandleFunc(route.method+" /organizations/{slug}"+route.path, func(w http.ResponseWriter, r *http.Request) {
			var account *identity.Account
			if a, ok := middleware.Account(r.Context()); ok {
				account = &a
			}
			membership, err := authorizer.Member(r.Context(), account, r.PathValue("slug"))
			if errors.Is(err, org.ErrNotFound) {
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
