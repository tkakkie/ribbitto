package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestReadingRollback(t *testing.T) {
	pool, scope := feedFixture(t)
	reading := unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter())
	for _, existing := range []bool{false, true} {
		want := []unread.Range{}
		if existing {
			feedRequire(t, reading.Feed(t.Context(), scope, 1, 2))
			want = []unread.Range{{Lo: 0, Hi: 3}}
		}
		// The missing topic fails after preparation writes the lock and join prefix.
		if err := reading.Topic(t.Context(), unread.TopicScope{Scope: scope}, 1, 3); err == nil {
			t.Fatal("missing topic did not fail")
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
	poster := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	for _, place := range [][2]kernel.ID{{f.Channel.ID, f.Channel.DefaultTopicID}, {f.Channel.ID, topic.ID}, {other.ID, other.DefaultTopicID}, {f.Channel.ID, f.Channel.DefaultTopicID}} {
		_, err := poster.PostToTopic(ctx, m, place[0], &place[1], "unread")
		feedRequire(t, err)
	}
	// A different organisation's higher committed sequence must not permit S=6.
	_, err := pool.Exec(ctx, "UPDATE organization SET event_seq=100,event_log_boundary_seq=100 WHERE id=$1", foreign.OrganizationID)
	feedRequire(t, err)
	handler, sessions, err := buildHandler(ctx, pool, handlerConfig{})
	feedRequire(t, err)
	token, _, err := sessions.Create(ctx, f.AccountID)
	feedRequire(t, err)
	outsider, _, err := sessions.Create(ctx, foreign.AccountID)
	feedRequire(t, err)
	scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
	feed := view.ChannelURL("read", f.Channel.ID)
	topicURL := view.ConversationURL("read", f.Channel.ID, &f.Channel.DefaultTopicID)
	request := func(method, path, cookie, origin string, form url.Values, hx bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
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
			{"foreign channel GET", "GET", view.ChannelURL("read", foreign.Channel.ID), token, "", "", 404},
			{"foreign topic GET", "GET", view.ConversationURL("read", f.Channel.ID, &foreign.Channel.DefaultTopicID), token, "", "", 404},
			{"other channel topic GET", "GET", view.ConversationURL("read", f.Channel.ID, &other.DefaultTopicID), token, "", "", 404},
			{"members GET", "GET", feed + "/members", token, "", "", 200},
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
				feedRanges(t, pool, scope, []unread.Range{})
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM channel_read")
				acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor")
			})
		}
		w := request("GET", base, token, "", nil, false)
		if w.Code != 200 {
			t.Fatalf("latest GET: %d %s", w.Code, w.Body.String())
		}
		feedRanges(t, pool, scope, []unread.Range{})
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM channel_read")
		acceptanceCount(t, pool, 0, "SELECT count(*) FROM topic_read_floor")
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
		feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: tc.end}})
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
		feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: tc.end}})
	}
}
