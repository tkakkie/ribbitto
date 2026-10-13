package web

import (
	"bytes"
	"context"
	"fmt"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// TestOrgRoutesAgainstPostgreSQL runs the real session and authorisation
// stores: whoever may not see an organisation gets a 404 that carries none
// of its data, on every route in orgRoutes.
func TestOrgRoutesAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	// Raw SQL on purpose: setup and sign-up create one organisation and join
	// everyone to it, so they cannot build a second organisation (globex, with
	// Bob) or an account with no membership (Carol, until she joins later).
	var acme, globex, alice, bob, carol kernel.ID
	for _, q := range []struct {
		dest *kernel.ID
		sql  string
	}{
		{&acme, "INSERT INTO organization (slug, name) VALUES ('acme', 'Acme Corporation') RETURNING id"},
		{&globex, "INSERT INTO organization (slug, name) VALUES ('globex', 'Globex Industries') RETURNING id"},
		{&alice, "INSERT INTO account (email, display_name, password_hash) VALUES ('alice@example.com', 'Alice', '$argon2id$x') RETURNING id"},
		{&bob, "INSERT INTO account (email, display_name, password_hash) VALUES ('bob@example.com', 'Bob', '$argon2id$x') RETURNING id"},
		{&carol, "INSERT INTO account (email, display_name, password_hash) VALUES ('carol@example.com', 'Carol', '$argon2id$x') RETURNING id"},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `WITH ids AS (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid)
		INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
		VALUES ($1, $3, 'owner', 1, 'alice'), ($2, $4, 'member', 1, 'bob')`, acme, globex, alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO setup (organization_id) VALUES ($1)", acme); err != nil {
		t.Fatal(err)
	}

	acmeFixture := conversationtest.Channel(t, pool, acme, "雑談", true)
	acmeChannel := acmeFixture.ID
	globexFixture := conversationtest.Channel(t, pool, globex, "雑談", true)
	globexChannel := globexFixture.ID
	defaultTopic := conversation.Topic{ID: acmeFixture.DefaultTopicID}
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	// Subtests stay sequential: they share this clock and add Carol's membership later.
	clock := now
	sessions := identitypg.NewSessions(pool, func() time.Time { return clock }, nil)
	token := func(account kernel.ID) string {
		t.Helper()
		value, _, err := sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	aliceToken, bobToken, carolToken, deletedToken := token(alice), token(bob), token(carol), token(alice)
	if err := sessions.Delete(ctx, deletedToken); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	services := postgresServices(t, pool, sessions, "", false)
	state := typing.New(realtime.NewHub())
	services.Typing = state
	budgetTime := now
	services.SignalLimits = middleware.NewMemberRateLimiter(func() time.Time { budgetTime = budgetTime.Add(time.Second); return budgetTime })
	membership, err := services.Authz.Member(ctx, &identity.Account{ID: alice}, "acme")
	if err != nil {
		t.Fatal(err)
	}
	place := typing.Place{Channel: acmeChannel, Topic: defaultTopic.ID}
	defer state.Open(acme, membership.Member.ID, acmeChannel)()
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	get := func(method, path, cookie string, at time.Time) *httptest.ResponseRecorder {
		clock = at
		return serveForm(handler, method, path, cookie, url.Values{"composer": {state.IssueComposer(acme, place, membership.Member.ID)}, "name": {"新しいチャンネル"}, "cursor": {"0"}, "body": {"posted through the page"}})
	}

	routes := orgRoutes(&pageRenderer{}, services.Channels, services.Topics, services.Messages, services.Posting, services.Branching, services.Stream, services.Reading, services.Typing, services.SignalLimits, services.ReadLimits, services.CreateLimits)
	if len(routes) == 0 {
		t.Fatal("no organisation routes")
	}
	for _, route := range routes {
		path := "/organizations/acme" + strings.ReplaceAll(route.path, "{$}", "")
		if strings.Contains(path, "{channelID}") {
			// Keep what follows the channel, such as a topic path.
			path = view.ChannelURL("acme", acmeChannel) + strings.TrimPrefix(route.path, "/channels/{channelID}")
		}
		path = strings.ReplaceAll(path, "{topicID}", strings.TrimPrefix(view.ConversationURL("acme", acmeChannel, &defaultTopic.ID), view.ChannelURL("acme", acmeChannel)+"/topics/"))
		for _, tt := range []struct {
			name   string
			cookie string
			at     time.Time
		}{
			{"signed out", "", now},
			{"account without a membership", carolToken, now},
			{"member of the other organisation", bobToken, now},
			{"deleted session", deletedToken, now},
			{"expired session", aliceToken, now.Add(identity.SessionLifetime)},
		} {
			t.Run(route.method+" "+path+" "+tt.name, func(t *testing.T) {
				before := state.Read(acme, place, kernel.ID{})
				w := get(route.method, path, tt.cookie, tt.at)
				if got := state.Read(acme, place, kernel.ID{}); got.Generation != before.Generation {
					t.Fatal("unauthorized request changed typing")
				}
				body := w.Body.String()
				if w.Code != http.StatusNotFound || strings.Contains(body, "Acme") || strings.Contains(body, "Globex") {
					t.Fatalf("status %d, body %q", w.Code, body)
				}
			})
		}
		t.Run(route.method+" "+path+" member", func(t *testing.T) {
			w := get(route.method, path, aliceToken, now)
			switch route.method + " " + route.path {
			case "GET /{$}":
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != view.ChannelURL("acme", acmeChannel) {
					t.Fatalf("default: %d %s", w.Code, w.Header().Get("Location"))
				}
			case "GET /channels/{channelID}/members", "GET /channels/{channelID}/overview", "GET /channels/{channelID}", "GET /channels/{channelID}/topics/{topicID}",
				"GET /channels/{channelID}/topics/{topicID}/members", "GET /channels/{channelID}/topics/{topicID}/overview":
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Acme Corporation") || !strings.Contains(w.Body.String(), "雑談") {
					t.Fatalf("channel: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels/{channelID}/overview", "POST /channels/{channelID}/topics/{topicID}/overview", "POST /channels/{channelID}", "POST /channels/{channelID}/topics/{topicID}":
				if w.Code != 303 || w.Header().Get("Location") != path {
					t.Fatalf("post: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels/{channelID}/topics/{topicID}/typing":
				if w.Code != 204 || w.Body.Len() != 0 {
					t.Fatalf("typing: %d", w.Code)
				}
			case "POST /channels/{channelID}/read", "POST /channels/{channelID}/topics/{topicID}/read":
				if w.Code != http.StatusNotFound {
					t.Fatalf("reading disabled: %d", w.Code)
				}
			case "GET /events":
				// This suite runs without Services.Stream, so a member gets the
				// stream-off 404 after authorisation; the stream itself is
				// tested through the production wiring in cmd/ribbitto.
				if w.Code != http.StatusNotFound {
					t.Fatalf("events: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels/{channelID}/branch":
				// The route reaches a member; an empty selection is refused.
				// Branching itself is tested in cmd/ribbitto and conversationpg.
				if w.Code != http.StatusUnprocessableEntity {
					t.Fatalf("branch: %d %s", w.Code, w.Body.String())
				}
			case "POST /channels":
				location := w.Header().Get("Location")
				rawID, ok := strings.CutPrefix(location, "/organizations/acme/channels/")
				id, valid := pathID(rawID)
				if w.Code != http.StatusSeeOther || !ok || !valid {
					t.Fatalf("create: %d %s", w.Code, location)
				}
				created, err := services.Channels.Get(ctx, org.Membership{Organization: org.Organization{ID: acme}}, id)
				if err != nil || created.Name != "新しいチャンネル" || created.OrganizationID != acme || location != view.ChannelURL("acme", created.ID) {
					t.Fatalf("created channel: %+v, %v; redirect %s", created, err, location)
				}
			default:
				t.Fatal("route lacks a success expectation")
			}
		})
	}

	t.Run("topic history and posting", func(t *testing.T) {
		clock = now
		named := conversationtest.Topic(t, pool, acme, acmeChannel, "Planning")
		other := conversationtest.Channel(t, pool, acme, "other", false)
		// Panels resolve the conversation's scope; each path changes one (#874).
		for _, path := range []string{
			view.ChannelURL("acme", globexChannel),                           // organisation
			view.ChannelURL("acme", kernel.ID{0xee}),                         // channel
			view.ConversationURL("acme", acmeChannel, &other.DefaultTopicID), // topic
		} {
			for _, panel := range []string{"/members", "/overview"} {
				if w := get("GET", path+panel, aliceToken, now); w.Code != 404 || strings.Contains(w.Body.String(), "Globex") {
					t.Errorf("panel %s%s: %d", path, panel, w.Code)
				}
			}
		}
		var initialCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id = $1", acme).Scan(&initialCount); err != nil {
			t.Fatal(err)
		}
		for _, ids := range [][2]kernel.ID{{acmeChannel, {}}, {acmeChannel, other.DefaultTopicID}, {{}, named.ID}, {globexChannel, named.ID}} {
			path := view.ConversationURL("acme", ids[0], &ids[1])
			for _, method := range []string{"GET", "POST"} {
				w := get(method, path, aliceToken, now)
				if w.Code != 404 {
					t.Fatalf("%s %v: %d", method, ids, w.Code)
				}
			}
			if w := get("GET", path+"?before=bad", aliceToken, now); w.Code != 404 {
				t.Fatalf("invalid paging with out-of-scope topic: %d", w.Code)
			}
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id = $1", acme).Scan(&count); err != nil || count != initialCount {
			t.Fatalf("invalid topic posts: %d, %v", count, err)
		}
		topicURL := view.ConversationURL("acme", acmeChannel, &named.ID)
		for i := 0; i < 51; i++ {
			w := serveForm(handler, "POST", topicURL, aliceToken, url.Values{"cursor": {"0"}, "body": {fmt.Sprintf("topic-message-%02d", i)}})
			if w.Code != 303 || w.Header().Get("Location") != topicURL {
				t.Fatalf("post: %d %s", w.Code, w.Body.String())
			}
		}
		latest := get("GET", topicURL, aliceToken, now)
		body := latest.Body.String()
		if latest.Code != 200 || strings.Count(body, `<li id="message-`) != 50 || strings.Contains(body, "topic-message-00") || !strings.Contains(body, "topic-message-50") || strings.Contains(body, "posted through the page") || !strings.Contains(body, `sse-connect="/organizations/acme/events?after=`) || strings.Contains(body, `hx-post="`+topicURL+`"`) || !strings.Contains(body, `action="`+topicURL+`"`) {
			t.Fatalf("latest topic: %d %s", latest.Code, body)
		}
		// The open topic, and only it, is the current link (Copilot on #320).
		if strings.Count(body, `aria-current="page"`) != 1 || !strings.Contains(body, `href="`+topicURL+`" aria-current="page"`) {
			t.Fatalf("current topic link: %s", body)
		}
		var before int64
		if err := pool.QueryRow(ctx, "SELECT event_seq FROM message WHERE organization_id = $1 AND topic_id = $2 AND body = 'topic-message-01'", acme, named.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		olderURL := fmt.Sprintf("%s?before=%d&unread=0", topicURL, before)
		if !strings.Contains(body, `href="`+strings.ReplaceAll(olderURL, "&", "&amp;")+`"`) {
			t.Fatal("paging link lost topic")
		}
		older := get("GET", olderURL, aliceToken, now)
		if older.Code != 200 || !strings.Contains(older.Body.String(), "topic-message-00") || strings.Contains(older.Body.String(), "topic-message-01") || !strings.Contains(older.Body.String(), `href="`+topicURL+`"`) {
			t.Fatalf("older: %d %s", older.Code, older.Body.String())
		}
		// The topic page's data attributes of the DOM contract (#351):
		// the topic on the list, and the loaded bound on the latest page
		// and after the last Load older.
		for _, page := range []struct {
			body, oldest string
		}{{body, fmt.Sprint(before)}, {older.Body.String(), "0"}} {
			doc, err := html.Parse(strings.NewReader(page.body))
			if err != nil {
				t.Fatal(err)
			}
			id := named.ID
			if got, ok := idAttribute(t, doc, "message-items", "data-topic"); !ok || got != fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]) {
				t.Errorf("data-topic = %q, %t; want the topic's ID", got, ok)
			}
			if got, _ := idAttribute(t, doc, "load-older", "data-oldest-seq"); got != page.oldest {
				t.Errorf("data-oldest-seq = %q; want %s", got, page.oldest)
			}
		}
		invalid := serveForm(handler, "POST", topicURL, aliceToken, url.Values{"cursor": {"0"}, "body": {""}})
		if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `action="`+topicURL+`"`) {
			t.Fatalf("invalid body: %d", invalid.Code)
		}
		feed := get("GET", view.ChannelURL("acme", acmeChannel), aliceToken, now)
		if strings.Contains(feed.Body.String(), `href="`+topicURL+`" aria-current`) {
			t.Fatal("the feed marks a topic as current")
		}
		if feed.Code != 200 || !strings.Contains(feed.Body.String(), `href="`+topicURL+`"`) {
			t.Fatal("topic missing from channel list")
		}
	})

	for _, scope := range []struct {
		name           string
		channel, topic kernel.ID
	}{
		{"channel organisation", globexChannel, globexFixture.DefaultTopicID},
		{"topic organisation", globexChannel, globexFixture.DefaultTopicID},
		{"channel", acmeChannel, conversationtest.Channel(t, pool, acme, "Other", false).DefaultTopicID},
		{"topic", acmeChannel, kernel.ID{99}},
	} {
		t.Run("typing/"+scope.name, func(t *testing.T) {
			isolated := services
			// Let the other lookup succeed, leaving exactly one predicate under test.
			if scope.name == "channel organisation" {
				isolated.Topics = typingTopics(func(ctx context.Context, m org.Membership, channel, topic kernel.ID) (conversation.Topic, error) {
					m.Organization.ID = globex
					return services.Topics.Get(ctx, m, channel, topic)
				})
			}
			if scope.name == "topic organisation" {
				isolated.Channels = &fakeChannels{channels: []conversation.Channel{{ID: globexChannel}}}
			}
			h, err := NewHandler("", catalogues, isolated)
			if err != nil {
				t.Fatal(err)
			}
			target := typing.Place{Channel: scope.channel, Topic: scope.topic}
			defer state.Open(acme, membership.Member.ID, scope.channel)()
			token := state.IssueComposer(acme, target, membership.Member.ID)
			w := serveForm(h, "POST", view.ConversationURL("acme", scope.channel, &scope.topic)+"/typing", aliceToken, url.Values{"composer": {fmt.Sprint(token)}})
			if w.Code != 404 || w.Body.String() != "404 page not found\n" || len(state.Read(acme, target, kernel.ID{}).Typists) != 0 {
				t.Fatalf("scope %s: %d", scope.name, w.Code)
			}
		})
	}
	t.Run("member cannot read another organisation's channel under own URL", func(t *testing.T) {
		w := get(http.MethodGet, view.ChannelURL("acme", globexChannel), aliceToken, now)
		if w.Code != http.StatusNotFound || w.Body.String() != "404 page not found\n" {
			t.Fatalf("status %d, body %q", w.Code, w.Body.String())
		}
	})
	for _, route := range routes {
		path := "/organizations/missing" + strings.ReplaceAll(route.path, "{$}", "")
		path = strings.ReplaceAll(path, "{channelID}", strings.TrimPrefix(view.ChannelURL("acme", acmeChannel), "/organizations/acme/channels/"))
		if w := get(route.method, path, aliceToken, now); w.Code != http.StatusNotFound {
			t.Fatalf("unknown organisation: %d", w.Code)
		}
	}

	t.Run("legacy path is unknown even for a member", func(t *testing.T) {
		w := get(http.MethodGet, "/o/acme/", aliceToken, now)
		if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" || w.Body.String() != "404 page not found\n" {
			t.Fatalf("status %d, location %q, body %q", w.Code, w.Header().Get("Location"), w.Body.String())
		}
	})

	// "/" sends a member of the setup organisation there, and only them.
	if w := get(http.MethodGet, "/", aliceToken, now); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/organizations/acme/" {
		t.Fatalf("alice at /: %d %q", w.Code, w.Header().Get("Location"))
	}
	for name, cookie := range map[string]string{"member of the other organisation only": bobToken, "signed out": ""} {
		if w := get(http.MethodGet, "/", cookie, now); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Acme") {
			t.Fatalf("%s at /: %d", name, w.Code)
		}
	}
	// Give Carol a membership only after the non-member assertions above.
	if _, err := pool.Exec(ctx, "INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle) VALUES ($1, $2, 'member', 2, 'carol')", acme, carol); err != nil {
		t.Fatal(err)
	}
	t.Run("author lookup isolation and reload", func(t *testing.T) {
		authzService := orgpg.NewAuthorizer(pool)
		a, err := authzService.Member(ctx, &identity.Account{ID: alice}, "acme")
		if err != nil {
			t.Fatal(err)
		}
		b, err := authzService.Member(ctx, &identity.Account{ID: bob}, "globex")
		if err != nil {
			t.Fatal(err)
		}
		var members map[kernel.ID]org.DirectoryEntry
		err = platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) (err error) {
			members, err = orgpg.MembersIn(snapshot).LookupMembers(ctx, acme, []kernel.ID{a.Member.ID, b.Member.ID})
			return err
		})
		if err != nil || len(members) != 1 || members[a.Member.ID].AccountID != alice || members[a.Member.ID].Handle != "alice" {
			t.Fatalf("members: %v, %v", members, err)
		}
		ids := []kernel.ID{}
		for _, m := range members {
			ids = append(ids, m.AccountID)
		}
		var names map[kernel.ID]string
		err = platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) (err error) {
			names, err = identitypg.AccountsIn(snapshot).LookupDisplayNames(ctx, ids)
			return err
		})
		if err != nil || len(names) != 1 || names[alice] != "Alice" {
			t.Fatalf("names: %v, %v", names, err)
		}
		posted := get("POST", view.ChannelURL("acme", acmeChannel), aliceToken, now)
		reloaded := get("GET", view.ChannelURL("acme", acmeChannel), carolToken, now)
		if posted.Code != 303 || reloaded.Code != 200 || !strings.Contains(reloaded.Body.String(), "posted through the page") || !strings.Contains(reloaded.Body.String(), "@alice") {
			t.Fatalf("page post/reload: %d / %d", posted.Code, reloaded.Code)
		}
		for _, path := range []string{view.ChannelURL("acme", acmeChannel), view.ChannelURL("globex", acmeChannel)} {
			if w := get("POST", path, bobToken, now); w.Code != 404 || w.Body.String() != "404 page not found\n" {
				t.Fatalf("foreign post: %d %s", w.Code, w.Body.String())
			}
		}
		poster := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
		for range 51 {
			if _, err := poster.Post(ctx, a, acmeChannel, "hello after reload"); err != nil {
				t.Fatal(err)
			}
		}
		w := get("GET", view.ChannelURL("acme", acmeChannel), carolToken, now)
		if w.Code != 200 || strings.Count(w.Body.String(), ">hello after reload</p>") != 50 || !strings.Contains(w.Body.String(), "@alice") {
			t.Fatalf("reload: %d %s", w.Code, w.Body.String())
		}
		w = get("GET", view.ChannelURL("acme", acmeChannel), bobToken, now)
		if w.Code != 404 || strings.Contains(w.Body.String(), "hello after reload") {
			t.Fatal("foreign member saw history")
		}
	})

	t.Run("image picker", func(t *testing.T) {
		clock = now
		// A sibling showing the same illustration must not count as the
		// changed channel's own image (#914).
		sibling := conversationtest.Channel(t, pool, acme, "Sibling", false)
		for _, denied := range []struct{ path, token string }{
			{view.ChannelURL("acme", acmeChannel), bobToken},
			{view.ChannelURL("acme", globexChannel), carolToken},
			{view.ChannelURL("acme", kernel.ID{0xee}), carolToken},
		} {
			if w := serveForm(handler, "POST", denied.path+"/overview", denied.token, url.Values{"image": {"moon"}}); w.Code != 404 {
				t.Fatalf("scope: %d", w.Code)
			}
		}
		var foreignImage *string
		if err := pool.QueryRow(ctx, "SELECT image FROM channel WHERE organization_id = $1 AND id = $2", globex, globexChannel).Scan(&foreignImage); err != nil || foreignImage != nil {
			t.Fatalf("foreign image: %v, %v", foreignImage, err)
		}
		for _, topic := range []*kernel.ID{nil, &defaultTopic.ID} {
			// Unrelated keys are dropped from the return; before and unread stay (#936).
			for _, query := range []struct{ sent, kept string }{{"", ""}, {"?before=2&unread=1&position=kept&position=twice", "?before=2&unread=1"}} {
				target := view.ConversationURL("acme", acmeChannel, topic) + "/overview" + query.sent
				back := view.ConversationURL("acme", acmeChannel, topic) + "/overview" + query.kept
				for _, hx := range []bool{false, true} {
					for _, key := range []string{"moon", "bad", ""} {
						request := httptest.NewRequest("POST", target, strings.NewReader(url.Values{"image": {key}, "organization_id": {fmt.Sprint(globex)}}.Encode()))
						request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: carolToken})
						if hx {
							request.Header.Set("HX-Request", "true")
						}
						response := httptest.NewRecorder()
						handler.ServeHTTP(response, request)
						want := key
						if key == "bad" {
							want = "moon"
							if response.Code != 422 || !strings.Contains(response.Body.String(), "Choose an image from the set.") || !strings.Contains(response.Body.String(), "<details open") {
								t.Fatalf("invalid image: %d", response.Code)
							}
						} else {
							if response.Code != 303 || response.Header().Get("Location") != back {
								t.Fatalf("return: %d %s", response.Code, response.Header().Get("Location"))
							}
							same := strings.TrimSuffix(path.Base(channelImageURL(acmeChannel, key)), ".svg")
							if _, err := pool.Exec(ctx, "UPDATE channel SET image = $1 WHERE organization_id = $2 AND id = $3", same, acme, sibling.ID); err != nil {
								t.Fatal(err)
							}
							page := serveForm(handler, "GET", target, carolToken, nil)
							doc, err := html.Parse(page.Body)
							if err != nil || page.Code != 200 {
								t.Fatalf("page: %d, %v", page.Code, err)
							}
							shown, radios := 0, 0
							for n := range doc.Descendants() {
								if n.DataAtom == atom.Input && attr(n, "name") == "image" {
									radios++
								}
								// Count only the changed channel's own places: its sidebar
								// link, the conversation header and the overview outside
								// the picker form.
								if n.DataAtom == atom.Img && attr(n, "src") == channelImageURL(acmeChannel, key) {
									own, inForm := false, false
									for a := n.Parent; a != nil; a = a.Parent {
										inForm = inForm || a.DataAtom == atom.Form
										own = own || attr(a, "id") == "link-"+view.ChannelDOMID(acmeChannel) || a.DataAtom == atom.Header || attr(a, "id") == "side-panel"
									}
									if own && !inForm {
										shown++
									}
								}
								if n.DataAtom == atom.Form && strings.Contains(attr(n, "action"), "/overview") {
									action, _ := url.Parse(attr(n, "action"))
									original, _ := url.Parse(back)
									if action.Path != original.Path || action.Query().Encode() != original.Query().Encode() {
										t.Fatal("picker lost conversation parameters")
									}
								}
							}
							if shown != 3 || radios != 2*(len(conversation.ChannelImageKeys())+1) {
								t.Fatalf("images=%d radios=%d", shown, radios)
							}
						}
						var stored *string
						if err := pool.QueryRow(ctx, "SELECT image FROM channel WHERE organization_id = $1 AND id = $2", acme, acmeChannel).Scan(&stored); err != nil || (want == "") != (stored == nil) || (stored != nil && *stored != want) {
							t.Fatalf("stored image: %v, %v", stored, err)
						}
					}
				}
			}
		}
		for _, key := range []string{"leaf", "bad", ""} {
			w := serveForm(handler, "POST", "/organizations/acme/channels", carolToken, url.Values{"name": {"image-" + key}, "image": {key}})
			if key == "bad" {
				var count int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM channel WHERE organization_id = $1 AND name = 'image-bad'", acme).Scan(&count); err != nil || count != 0 || w.Code != 422 {
					t.Fatalf("invalid creation: %d, %d, %v", count, w.Code, err)
				}
			} else {
				id, ok := pathID(strings.TrimPrefix(w.Header().Get("Location"), "/organizations/acme/channels/"))
				c, err := services.Channels.Get(ctx, membership, id)
				if w.Code != 303 || !ok || err != nil || c.Image != key {
					t.Fatalf("creation: %d, %+v, %v", w.Code, c, err)
				}
			}
		}
	})
	if logs.Len() != 0 {
		t.Errorf("unexpected message fallback: %s", logs.String())
	}
}

func serveForm(handler http.Handler, method, path, cookie string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// oneOrganisation makes the "live" session's account the owner of acme.
type oneOrganisation struct{}

func (oneOrganisation) Member(_ context.Context, account *identity.Account, slug string) (org.Membership, error) {
	if account == nil || slug != "acme" {
		return org.Membership{}, org.ErrNotFound
	}
	return org.Membership{Organization: org.Organization{Slug: "acme", Name: "Acme Corporation"}, Member: org.Member{Role: org.RoleOwner, Handle: "alice"}}, nil
}

func (oneOrganisation) HomeSlug(context.Context, *identity.Account) (string, error) {
	return "acme", nil
}

func TestHomeSignUpLink(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, open := range []bool{true, false} {
		handler, err := NewHandler("", catalogues, testServices(func(s *Services) {
			s.SignUp, s.SetupSessions = fakeSignUp{&fakeSetup{open: open}}, &fakeSetup{}
		}))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `href="/signin"`) || strings.Contains(body, `href="/signup"`) != open {
			t.Fatalf("sign-up open %v: status %d, body %s", open, w.Code, body)
		}
	}
}

func TestChannelRendering(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice))
	if err != nil {
		t.Fatal(err)
	}
	for lang, texts := range map[string][]string{
		// The spaces around the member's name are part of the checked text.
		"en": {"Acme Corporation", `Signed in as <bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span> · Owner`, "Sign out"},
		"ja": {"Acme Corporation", `サインイン中: <bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span> · オーナー`, "サインアウト"},
	} {
		r := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", kernel.ID{1}), nil)
		r.Header.Set("Accept-Language", lang)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		// The signed-in organisation page gets the same security headers
		// and script nonces as every other page (TestHTMLSecurity).
		nonce := responseNonce(t, w)
		if scripts := strings.Count(body, "<script "); scripts < 5 || strings.Count(body, ` nonce="`+nonce+`"`) != scripts {
			t.Errorf("%s: scripts lack the response nonce", lang)
		}
		if w.Code != http.StatusOK || !strings.Contains(body, `<form method="post" action="/signout">`) {
			t.Fatalf("%s: status %d, body %s", lang, w.Code, body)
		}
		if !strings.Contains(body, "雑談 &lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(body, "<script>alert(1)</script>") {
			t.Fatal("channel name was not escaped")
		}
		doc, err := html.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		selected := 0
		for n := range doc.Descendants() {
			if attr(n, "aria-current") == "page" {
				selected++
				if n.DataAtom != atom.A || attr(n, "href") != view.ChannelURL("acme", kernel.ID{1}) {
					t.Fatal("wrong current channel link")
				}
				if !slices.Contains(strings.Fields(attr(n, "class")), "text-on-selected") {
					t.Error("selected link lacks text-on-selected")
				}
				// The brand pill marks the current row besides its text colour.
				pill := false
				for c := range n.Descendants() {
					pill = pill || c.DataAtom == atom.Span && slices.Contains(strings.Fields(attr(c, "class")), "bg-brand")
				}
				if !pill {
					t.Error("selected link lacks the brand selection pill")
				}
			}
		}
		if selected != 1 {
			t.Fatalf("%d selected channels", selected)
		}

		for _, text := range texts {
			if !strings.Contains(body, text) {
				t.Errorf("%s page lacks %q", lang, text)
			}
		}
	}
}
