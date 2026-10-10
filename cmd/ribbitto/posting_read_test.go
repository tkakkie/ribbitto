package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestPostingRead(t *testing.T) {
	for _, mode := range []string{"feed", "topic clear", "topic post", "topic move", "rollback", "history rollback", "future cursor"} {
		t.Run(mode, func(t *testing.T) {
			pool, s := feedFixture(t) // committed messages 2–6
			s.MemberID = orgtest.Member(t, pool, s.OrganizationID, identitytest.Account(t, pool, "poster@example.org", "Poster"), org.RoleMember, "poster", 1)
			m := org.Membership{Organization: org.Organization{ID: s.OrganizationID}, Member: org.Member{ID: s.MemberID, JoinedEventSeq: 1}}
			a := conversationtest.Topic(t, pool, s.OrganizationID, s.ChannelID, "a")
			// Message 2 was shown in A; messages 3–6 in other topics stay unread.
			_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND event_seq=2", s.OrganizationID, a.ID)
			feedRequire(t, err)
			var topic *kernel.ID
			cursor, floor := int64(3), int64(7)
			want := []unread.Range{{Lo: 0, Hi: 4}, {Lo: 7, Hi: 8}}
			if mode == "topic clear" || mode == "topic post" || mode == "topic move" {
				topic = &a.ID
				want[0].Hi = 3
				if mode != "topic clear" {
					floor = cursor
					seq, moved := int64(5), int64(0)
					if mode == "topic move" {
						seq, moved = 3, 5
					}
					_, err = pool.Exec(t.Context(), "UPDATE message SET topic_id=$2,moved_event_seq=nullif($4,0) WHERE organization_id=$1 AND event_seq=$3", s.OrganizationID, a.ID, seq, moved)
					feedRequire(t, err)
				}
			}
			events := postingEvents
			if mode == "rollback" || mode == "history rollback" {
				events = func(tx platform.Tx) conversation.EventAppender { return failingNoticeEvents{postingEvents(tx)} }
			}
			if mode == "future cursor" {
				cursor = 7
			}
			posting := conversationpg.NewPagePosting(pool, postingSequence, events, postReads, nil)
			if mode == "history rollback" {
				_, err = posting.PostToTopic(t.Context(), m, s.ChannelID, topic, "own post")
			} else {
				_, err = posting.PostFromPage(t.Context(), m, s.ChannelID, topic, "own post", cursor)
			}
			switch mode {
			case "rollback", "history rollback", "future cursor":
				expected := errBranchNoticeAppend
				if mode == "future cursor" {
					expected = conversation.ErrInvalidPostCursor
				}
				if !errors.Is(err, expected) {
					t.Fatal(err)
				}
				feedRanges(t, pool, s, []unread.Range{})
				var locks, committed int64
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM channel_read WHERE organization_id=$1 AND member_id=$2", s.OrganizationID, s.MemberID).Scan(&locks))
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id=$1", s.OrganizationID).Scan(&committed))
				if locks != 0 || committed != 6 {
					t.Fatalf("rollback: locks=%d cursor=%d", locks, committed)
				}
			default:
				feedRequire(t, err)
				feedRanges(t, pool, s, want) // own 7 read; unseen intervening messages unread
				if topic != nil {
					topicFloor(t, pool, unread.TopicScope{Scope: s, TopicID: *topic}, floor)
				}
			}
		})
	}
}

func TestHistoryPostingRead(t *testing.T) {
	for _, kind := range []string{"feed", "topic"} {
		t.Run(kind, func(t *testing.T) {
			pool, scope := feedFixture(t) // Other author's messages 2–6.
			account := identitytest.Account(t, pool, "poster@example.org", "Poster")
			scope.MemberID = orgtest.Member(t, pool, scope.OrganizationID, account, org.RoleMember, "poster", 1)
			var topic kernel.ID
			feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE organization_id=$1 AND id=$2", scope.OrganizationID, scope.ChannelID).Scan(&topic))
			other := conversationtest.Topic(t, pool, scope.OrganizationID, scope.ChannelID, "other")
			_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND event_seq IN (3,5)", scope.OrganizationID, other.ID)
			feedRequire(t, err)
			// Keep an existing read message and topic floor as well as unread gaps.
			feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				return newTopicWriter().Read(t.Context(), tx, unread.TopicScope{Scope: scope, TopicID: topic}, 1, 2)
			}))
			handler, sessions, err := buildHandler(t.Context(), pool, handlerConfig{})
			feedRequire(t, err)
			token, _, err := sessions.Create(t.Context(), account)
			feedRequire(t, err)
			path := view.ChannelURL("feed", scope.ChannelID)
			if kind == "topic" {
				path = view.ConversationURL("feed", scope.ChannelID, &topic)
			}
			request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			page := request("GET", path+"?before=3", nil)
			if page.Code != 200 || strings.Contains(page.Body.String(), `name="cursor"`) || !strings.Contains(page.Body.String(), `action="`+path+`"`) {
				t.Fatalf("history composer: %d %s", page.Code, page.Body.String())
			}
			for _, cursor := range [][]string{{""}, {"bad"}, {"0", "0"}, {"-1"}, {"7"}} {
				w := request("POST", path, url.Values{"body": {"own post"}, "cursor": cursor})
				if w.Code != 400 {
					t.Fatalf("cursor %v: status=%d, want 400", cursor, w.Code)
				}
				feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 3}})
				topicFloor(t, pool, unread.TopicScope{Scope: scope, TopicID: topic}, 2)
			}
			w := request("POST", path, url.Values{"body": {"own post"}})
			if w.Code != 303 || w.Header().Get("Location") != path {
				t.Fatalf("history post: %d %s", w.Code, w.Body.String())
			}
			// Only own message 7 is newly read; all messages 2–6 retain their state.
			feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 3}, {Lo: 7, Hi: 8}})
			topicFloor(t, pool, unread.TopicScope{Scope: scope, TopicID: topic}, 2)
			acceptanceCount(t, pool, 1, "SELECT count(*) FROM topic_read_floor")
			acceptanceCount(t, pool, 1, "SELECT count(*) FROM message WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND event_seq=7", scope.OrganizationID, scope.ChannelID, scope.MemberID)
		})
	}
}
