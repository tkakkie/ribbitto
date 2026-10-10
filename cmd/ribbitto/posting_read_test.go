package main

import (
	"errors"
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
)

func TestPostingRead(t *testing.T) {
	for _, mode := range []string{"feed", "topic clear", "topic post", "topic move", "rollback", "future cursor"} {
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
			if mode == "rollback" {
				events = func(tx platform.Tx) conversation.EventAppender { return failingNoticeEvents{postingEvents(tx)} }
			}
			if mode == "future cursor" {
				cursor = 7
			}
			_, err = conversationpg.NewPagePosting(pool, postingSequence, events, postReads, nil).PostFromPage(t.Context(), m, s.ChannelID, topic, "own post", cursor)
			switch mode {
			case "rollback", "future cursor":
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
