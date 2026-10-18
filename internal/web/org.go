package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/typing"
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
func orgRoutes(pages *pageRenderer, channels Channels, topics TopicLookup, messages MessageReader, posting *conversation.Posting, branching Branching, pinning Pinning, stream *Streaming, reading Reading, typingState *typing.State, limiter, readLimit *middleware.MemberRateLimiter, createLimit *middleware.MemberRateLimiter, resync SidebarResync, resyncLimit, branchLimit, postLimit, pinLimit *middleware.MemberRateLimiter, search MemberSearch, mentionLimit *middleware.MemberRateLimiter) []orgRoute {
	handlers := channelPages{typing: typingState, limiter: limiter, readLimit: readLimit, createLimit: createLimit, branchLimit: branchLimit, postLimit: postLimit, pinLimit: pinLimit, reading: reading, pages: pages, channels: channels, topics: topics, messages: messages, posting: posting, branching: branching, pinning: pinning, stream: stream}
	if stream != nil {
		handlers.renders = newRenderCache(stream.Lifetime)
	}
	return []orgRoute{
		{http.MethodGet, "/{$}", handlers.home},
		{http.MethodGet, "/events", handlers.events},
		{http.MethodGet, "/mention-candidates", mentionCandidates(search, mentionLimit)},
		{http.MethodPost, "/sidebar/resync", resyncSidebar(resync, resyncLimit)},
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
