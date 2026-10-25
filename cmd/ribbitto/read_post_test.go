package main

import (
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"github.com/tkakkie/ribbitto/internal/web"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestReadingRollback(t *testing.T) {
	pool, scope := feedFixture(t)
	reading := unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
	for _, existing := range []bool{false, true} {
		want := []conversation.SequenceRange{}
		if existing {
			feedRequire(t, reading.Feed(t.Context(), scope, 1, 2))
			want = []conversation.SequenceRange{{Lo: 0, Hi: 3}}
		}
		before := readPostSnapshot(t, pool, nil)
		// The missing topic fails after preparation writes the lock and join prefix.
		if err := reading.Topic(t.Context(), unread.TopicScope{Scope: scope}, 1, 3); err == nil {
			t.Fatal("missing topic did not fail")
		}
		if err := reading.TopicShown(t.Context(), unread.TopicScope{Scope: scope}, 1, 3, 2, 3); err == nil {
			t.Fatal("missing topic run did not fail")
		}
		if readPostSnapshot(t, pool, nil) != before {
			t.Fatal("rollback changed read state")
		}
		feedRanges(t, pool, scope, want)
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor")
		if !existing {
			acceptanceCount(t, pool, 0, "SELECT count(*) FROM channel_read")
		}
	}
}

func TestReadPost(t *testing.T) {
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "read", "general")
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "other topic")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	poster := conversationpg.NewPosting(pool, compose.PostingSequence, compose.PostingEvents, nil)
	for _, place := range [][2]kernel.ID{{f.Channel.ID, f.Channel.DefaultTopicID}, {f.Channel.ID, topic.ID}, {other.ID, other.DefaultTopicID}, {f.Channel.ID, f.Channel.DefaultTopicID}} {
		_, err := poster.PostToTopic(ctx, m, place[0], &place[1], "unread")
		feedRequire(t, err)
	}
	// A different organisation's higher committed sequence must not permit S=6.
	_, err := pool.Exec(ctx, "UPDATE organization SET event_seq=100,event_log_boundary_seq=100 WHERE id=$1", foreign.OrganizationID)
	feedRequire(t, err)
	hub := realtime.NewHub()
	// Validation sequences intentionally exceed the production read burst.
	clock := time.Unix(1, 0)
	advance := true
	handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: hub, memberLimits: web.NewMemberLimits(time.Now, web.WithReadAdmission(func() time.Time {
		if advance {
			clock = clock.Add(time.Second)
		}
		return clock
	}, 4))})
	feedRequire(t, err)
	token, _, err := sessions.Create(ctx, f.AccountID)
	feedRequire(t, err)
	outsider, _, err := sessions.Create(ctx, foreign.AccountID)
	feedRequire(t, err)
	scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
	feed := view.ChannelURL("read", f.Channel.ID)
	topicURL := view.ConversationURL("read", f.Channel.ID, &f.Channel.DefaultTopicID)
	request := func(method, path, cookie, origin string, form url.Values, hx bool) *httptest.ResponseRecorder {
		before := readPostSnapshot(t, pool, nil)
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 204 && w.Code != 303 && readPostSnapshot(t, pool, nil) != before {
			t.Fatalf("refusal %d changed read state", w.Code)
		}
		return w
	}
	// Production route callers share one budget even across channels and topics.
	for i, place := range [][2]kernel.ID{{f.Channel.ID, f.Channel.DefaultTopicID}, {other.ID, other.DefaultTopicID}, {f.Channel.ID, topic.ID}, {other.ID, other.DefaultTopicID}, {f.Channel.ID, topic.ID}} {
		w := request("POST", view.ConversationURL("read", place[0], &place[1])+"/typing", token, "", url.Values{"composer": {"invalid"}}, i%2 == 0)
		want := 204
		if i == 4 {
			want = 429
		}
		if w.Code != want || w.Body.Len() != 0 {
			t.Fatalf("production typing caller %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for _, base := range []string{feed, topicURL} {
		for _, tc := range []struct {
			name, method, path, cookie, origin, cursor string
			status                                     int
		}{
			{"CSRF", "POST", base + "/read", token, "https://elsewhere.invalid", "3", 403},
			{"future cursor", "POST", base + "/read", token, "", "6", 400},
			{"non-member GET", "GET", base, outsider, "", "3", 404},
			{"non-member POST", "POST", base + "/read", outsider, "", "3", 404},
			{"older GET", "GET", base + "?before=3", token, "", "3", 200},
			{"older POST", "POST", base + "/read?before=3", token, "", "3", 400},
			{"around GET", "GET", base + "?around=3", token, "", "3", 200},
			{"around POST", "POST", base + "/read?around=3", token, "", "3", 400},
			{"foreign channel GET", "GET", view.ChannelURL("read", foreign.Channel.ID), token, "", "", 404},
			{"foreign topic GET", "GET", view.ConversationURL("read", f.Channel.ID, &foreign.Channel.DefaultTopicID), token, "", "", 404},
			{"other channel topic GET", "GET", view.ConversationURL("read", f.Channel.ID, &other.DefaultTopicID), token, "", "", 404},
			// A panel beside the latest conversation keeps its read form; beside an older page it adds none (#874).
			{"older members GET", "GET", base + "/members?before=3&unread=0", token, "", "", 200},
			{"non-member members GET", "GET", feed + "/members", outsider, "", "", 404},
			{"unseen channel", "POST", view.ChannelURL("read", foreign.Channel.ID) + "/read", token, "", "3", 404},
			{"topic of another channel", "POST", view.ConversationURL("read", f.Channel.ID, &other.DefaultTopicID) + "/read", token, "", "3", 404},
			{"unknown topic", "POST", view.ConversationURL("read", f.Channel.ID, &kernel.ID{}) + "/read", token, "", "3", 404},
		} {
			t.Run(base+"/"+tc.name, func(t *testing.T) {
				w := request(tc.method, tc.path, tc.cookie, tc.origin, url.Values{"cursor": {tc.cursor}}, false)
				if w.Code != tc.status {
					t.Fatalf("status=%d, want %d: %s", w.Code, tc.status, w.Body.String())
				}
				if tc.method == "GET" && strings.Contains(w.Body.String(), `id="read-form"`) {
					t.Fatal("older or unauthorized page offers a read form")
				}
				feedRanges(t, pool, scope, []conversation.SequenceRange{})
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM channel_read")
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor")
			})
		}
		w := request("GET", base, token, "", nil, false)
		if w.Code != 200 {
			t.Fatalf("latest GET: %d %s", w.Code, w.Body.String())
		}
		feedRanges(t, pool, scope, []conversation.SequenceRange{})
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM channel_read")
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor")
	}
	for _, base := range []string{feed, topicURL} {
		for _, tc := range []struct{ name, form, suffix string }{
			{"half from", "cursor=5&from=2", ""}, {"half through", "cursor=5&through=2", ""},
			{"all with run", "cursor=5&scope=all&from=2&through=3", ""}, {"bad scope", "cursor=5&scope=x", ""},
			{"through above cursor", "cursor=3&from=2&through=5", ""}, {"cursor above committed", "cursor=6&from=2&through=3", ""},
			{"from above through", "cursor=5&from=3&through=2", ""}, {"from below one", "cursor=5&from=0&through=2", ""},
			{"cursor repeated", "cursor=5&cursor=5&from=2&through=3", ""},
			{"from repeated", "cursor=5&from=2&from=2&through=3", ""},
			{"through repeated", "cursor=5&from=2&through=3&through=3", ""},
			{"scope repeated", "cursor=5&scope=all&scope=all", ""},
			{"cursor negative", "cursor=-1&from=2&through=3", ""},
			{"cursor MaxInt64", "cursor=9223372036854775807&from=2&through=3", ""},
			{"run before", "cursor=5&from=2&through=3", "?before="}, {"run around", "cursor=5&from=2&through=3", "?around="},
		} {
			t.Run(base+"/"+tc.name, func(t *testing.T) {
				form, _ := url.ParseQuery(tc.form)
				// The fixed body keeps internal error text (the wrapped
				// ErrInvalidCursor reason, the offending value) off the wire.
				if w := request("POST", base+"/read"+tc.suffix, token, "", form, false); w.Code != 400 || w.Body.String() != "Bad Request\n" {
					t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
				}
			})
		}
		// Specification 5.2: an unresolvable place answers 404 before the form
		// is judged, so a refused form must not reveal that the place exists.
		for _, place := range []struct{ name, path string }{
			{"unseen channel", view.ChannelURL("read", foreign.Channel.ID)},
			{"other channel topic", view.ConversationURL("read", f.Channel.ID, &other.DefaultTopicID)},
			{"unknown topic", view.ConversationURL("read", f.Channel.ID, &kernel.ID{})},
		} {
			for _, bad := range []string{"cursor=x", "cursor=5&from=2", "cursor=5&through=2", "cursor=5&from=x&through=3", "cursor=5&scope=x", "from=2&through=3"} {
				t.Run(base+"/404 before 400/"+place.name+"/"+bad, func(t *testing.T) {
					form, _ := url.ParseQuery(bad)
					if w := request("POST", place.path+"/read", token, "", form, false); w.Code != 404 {
						t.Fatalf("status=%d want 404", w.Code)
					}
				})
			}
		}
		for _, form := range []string{"cursor=5&from=2&through=3", "cursor=5&scope=all"} {
			values, _ := url.ParseQuery(form)
			for _, tc := range []struct {
				name, path, cookie, origin string
				status                     int
			}{
				{"non-member", base + "/read", outsider, "", 404},
				{"unseen channel", view.ChannelURL("read", foreign.Channel.ID) + "/read", token, "", 404},
				{"other channel topic", view.ConversationURL("read", f.Channel.ID, &other.DefaultTopicID) + "/read", token, "", 404},
				{"CSRF", base + "/read", token, "https://elsewhere.invalid", 403},
			} {
				t.Run(base+"/"+form+"/"+tc.name, func(t *testing.T) {
					if w := request("POST", tc.path, tc.cookie, tc.origin, values, false); w.Code != tc.status {
						t.Fatalf("status=%d want=%d", w.Code, tc.status)
					}
				})
			}
		}
	}
	// Each successful scope must leave the other channel/member and the future unread.
	for _, tc := range []struct {
		base string
		hx   bool
		end  int64
	}{{topicURL, true, 3}, {topicURL, false, 3}, {feed, true, 5}, {feed, false, 5}} {
		w := request("GET", tc.base, token, "", nil, false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `name="cursor" value="5"`) || !strings.Contains(w.Body.String(), `action="`+tc.base+`/read"`) {
			t.Fatalf("snapshot form: %d %s", w.Code, w.Body.String())
		}
		id := foreign.OrganizationID
		form := url.Values{"cursor": {"3"}, "organization_id": {fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])}}
		w = request("POST", tc.base+"/read", token, "", form, tc.hx)
		want := 303
		if tc.hx {
			want = 204
		}
		if w.Code != want || (!tc.hx && w.Header().Get("Location") != tc.base) {
			t.Fatalf("read: %d %s", w.Code, w.Body.String())
		}
		feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: tc.end}})
		acceptanceCount(t, pool, 1, "SELECT count(*) FROM channel_read")
		// The topic POST (run first) raised only its own topic's floor; the
		// other topic's message 3 stayed outside its [0, 3) range.
		acceptanceCount(t, pool, 1, "SELECT count(*) FROM topic_read_floor WHERE topic_id=$1 AND floor_seq=3", f.Channel.DefaultTopicID)
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor WHERE topic_id=$1", topic.ID)
		// Refusal after a successful write must preserve existing state as well.
		w = request("POST", tc.base+"/read", token, "", url.Values{"cursor": {"6"}}, tc.hx)
		if w.Code != 400 {
			t.Fatalf("future cursor: %d", w.Code)
		}
		feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: tc.end}})
	}
	// Populated witnesses differ in member, channel or organisation. Another
	// topic shares ranges, so its floor and unread message are checked separately.
	reading := unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
	b := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "reader@example.org", "B"), org.RoleMember, "reader", 1)
	for _, witness := range []unread.TopicScope{
		{Scope: unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: b}, TopicID: topic.ID},
		{Scope: unread.Scope{OrganizationID: f.OrganizationID, ChannelID: other.ID, MemberID: f.MemberID}, TopicID: other.DefaultTopicID},
		{Scope: unread.Scope{OrganizationID: foreign.OrganizationID, ChannelID: foreign.Channel.ID, MemberID: foreign.MemberID}, TopicID: foreign.Channel.DefaultTopicID},
	} {
		feedRequire(t, reading.Topic(ctx, witness, 1, 5))
	}
	for _, base := range []string{feed, topicURL} {
		for _, form := range []string{"cursor=5&from=5&through=5", "cursor=3&scope=all"} {
			t.Run(base+"/success/"+form, func(t *testing.T) {
				_, err := pool.Exec(ctx, "DELETE FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3", scope.OrganizationID, scope.ChannelID, scope.MemberID)
				feedRequire(t, err)
				before := readPostSnapshot(t, pool, &scope)
				values, _ := url.ParseQuery(form)
				values.Set("organization_id", fmt.Sprint(foreign.OrganizationID))
				values.Set("channel_id", fmt.Sprint(other.ID))
				values.Set("topic_id", fmt.Sprint(topic.ID))
				if w := request("POST", base+"/read", token, "", values, true); w.Code != 204 {
					t.Fatalf("status=%d", w.Code)
				}
				want := []conversation.SequenceRange{{Lo: 0, Hi: 2}, {Lo: 4, Hi: 6}}
				if values.Has("scope") {
					want = []conversation.SequenceRange{{Lo: 0, Hi: 5}}
					if base == topicURL {
						want[0].Hi = 3
					}
				}
				feedRanges(t, pool, scope, want)
				if readPostSnapshot(t, pool, &scope) != before {
					t.Fatal("read changed another member/channel/organisation")
				}
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor WHERE member_id=$1 AND topic_id=$2", scope.MemberID, topic.ID)
				if !values.Has("scope") {
					acceptanceCount(t, pool, 0, "SELECT count(*) FROM read_range WHERE member_id=$1 AND channel_id=$2 AND lo<=3 AND 3<hi", scope.MemberID, scope.ChannelID)
				}
				// Refuse history bounds against populated state too.
				if w := request("POST", base+"/read?before=", token, "", values, true); w.Code != 400 {
					t.Fatalf("populated refusal: %d", w.Code)
				}
			})
		}
	}
	clock = clock.Add(10 * time.Second)
	advance = false
	for i := range 17 {
		w := request("POST", feed+"/read", token, "", url.Values{"cursor": {"0"}, "from": {"1"}, "through": {"1"}}, true)
		want := 400
		if i == 16 {
			want = 429
		}
		if w.Code != want || (want == 429 && w.Header().Get("Retry-After") != "1") {
			t.Fatalf("run admission %d: %d", i, w.Code)
		}
	}
}

func readPostSnapshot(t *testing.T, pool *pgxpool.Pool, excluded *unread.Scope) string {
	t.Helper()
	scope := unread.Scope{}
	if excluded != nil {
		scope = *excluded
	}
	var result string
	feedRequire(t, pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(data ORDER BY data),'[]'::jsonb)::text FROM (
	 SELECT jsonb_build_array('channel_read',to_jsonb(r)) data FROM channel_read r WHERE NOT (organization_id=$1 AND channel_id=$2 AND member_id=$3)
	 UNION ALL SELECT jsonb_build_array('read_range',to_jsonb(r)) FROM read_range r WHERE NOT (organization_id=$1 AND channel_id=$2 AND member_id=$3)
	 UNION ALL SELECT jsonb_build_array('topic_read_floor',to_jsonb(r)) FROM topic_read_floor r WHERE NOT (organization_id=$1 AND channel_id=$2 AND member_id=$3)
	) state`, scope.OrganizationID, scope.ChannelID, scope.MemberID).Scan(&result))
	return result
}
