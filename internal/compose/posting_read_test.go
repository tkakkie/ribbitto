package compose_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type failingPostAppend struct{ conversation.EventAppender }

func (e failingPostAppend) Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	// An appender can write before returning an error; that event must roll back too.
	if err := e.EventAppender.Append(ctx, organizationID, seq, kind, audience, data); err != nil {
		return err
	}
	return errBranchNoticeAppend
}

func TestPostingRead(t *testing.T) {
	for _, mode := range []string{"feed", "topic clear", "topic post", "topic move", "topic rollback", "topic history rollback", "topic future cursor", "rollback", "history rollback", "future cursor"} {
		t.Run(mode, func(t *testing.T) {
			pool, s := feedFixture(t) // committed messages 2–6
			s.MemberID = orgtest.Member(t, pool, s.OrganizationID, identitytest.Account(t, pool, "poster@example.org", "Poster"), org.RoleMember, "poster", 1)
			m := org.Membership{Organization: org.Organization{ID: s.OrganizationID}, Member: org.Member{ID: s.MemberID, JoinedEventSeq: 1}}
			a := conversationtest.Topic(t, pool, s.OrganizationID, s.ChannelID, "a")
			// Message 2 was shown in A; messages 3–6 in other topics stay unread.
			_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND event_seq=2", s.OrganizationID, a.ID)
			feedRequire(t, err)
			var topic *kernel.ID
			cursor := int64(3)
			// Decision 5: only own 7 is read; the page scope and floors stay unchanged.
			want := []conversation.SequenceRange{{Lo: 0, Hi: 2}, {Lo: 7, Hi: 8}}
			if strings.HasPrefix(mode, "topic ") {
				topic = &a.ID
				if mode == "topic post" || mode == "topic move" {
					seq, moved := int64(5), int64(0)
					if mode == "topic move" {
						seq, moved = 3, 5
					}
					_, err = pool.Exec(t.Context(), "UPDATE message SET topic_id=$2,moved_event_seq=nullif($4,0) WHERE organization_id=$1 AND event_seq=$3", s.OrganizationID, a.ID, seq, moved)
					feedRequire(t, err)
				}
			}
			events := compose.PostingEvents
			if strings.Contains(mode, "rollback") {
				events = func(tx platform.Tx) conversation.EventAppender { return failingNoticeEvents{compose.PostingEvents(tx)} }
			}
			if mode == "topic rollback" || mode == "topic history rollback" {
				events = func(tx platform.Tx) conversation.EventAppender { return failingPostAppend{compose.PostingEvents(tx)} }
			}
			if strings.HasSuffix(mode, "future cursor") {
				cursor = 7
			}
			// An existing lock row isolates the own-message union cost.
			if topic != nil && !strings.Contains(mode, "rollback") && !strings.HasSuffix(mode, "future cursor") {
				_, err = pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
			}
			var steps []string
			trace := &pageTrace{QueryCounter: platform.NewQueryCounter(), names: map[string]int{}, hook: func(name string) { steps = append(steps, name) }}
			posting := conversationpg.NewPagePosting(readTracedPool(t, pool, trace), compose.PostingSequence, events, compose.PostReads, compose.LookupMembers, nil)
			if strings.HasSuffix(mode, "history rollback") {
				_, err = posting.PostToTopic(t.Context(), m, s.ChannelID, topic, "own post")
			} else {
				_, err = posting.Send(t.Context(), m, s.ChannelID, conversation.Send{Topic: topic, Body: "own post", Cursor: &cursor})
			}
			t.Logf("statements=%+v names=%v", trace.Counts(), steps)
			switch mode {
			case "rollback", "history rollback", "future cursor", "topic rollback", "topic history rollback", "topic future cursor":
				expected := errBranchNoticeAppend
				if strings.HasSuffix(mode, "future cursor") {
					expected = conversation.ErrInvalidPostCursor
				}
				if !errors.Is(err, expected) {
					t.Fatalf("posting error = %v; want %v", err, expected)
				}
				if strings.HasSuffix(mode, "future cursor") && !slices.Equal(steps, []string{"NextEventSeq"}) {
					t.Fatalf("future cursor prepared read state: %v", steps)
				}
				var floors, messages, events int64
				feedRequire(t, pool.QueryRow(t.Context(), `SELECT
					(SELECT count(*) FROM topic_read_floor WHERE organization_id=$1 AND member_id=$2),
					(SELECT count(*) FROM message WHERE organization_id=$1 AND member_id=$2),
					(SELECT count(*) FROM event_log WHERE organization_id=$1 AND seq=7)`, s.OrganizationID, s.MemberID).Scan(&floors, &messages, &events))
				if floors != 0 || messages != 0 || events != 0 {
					t.Errorf("rollback retained floors=%d messages=%d events=%d; want zero", floors, messages, events)
				}
				feedRanges(t, pool, s, []conversation.SequenceRange{})
				var locks, committed int64
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM channel_read WHERE organization_id=$1 AND member_id=$2", s.OrganizationID, s.MemberID).Scan(&locks))
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id=$1", s.OrganizationID).Scan(&committed))
				if locks != 0 || committed != 6 {
					t.Fatalf("rollback: locks=%d cursor=%d", locks, committed)
				}
				counts := trace.Counts()
				if counts.Begins != 1 || counts.Commits != 0 || counts.Rollbacks != 1 {
					t.Fatalf("error transaction = %+v", counts)
				}
				if mode == "topic rollback" && trace.names["InsertReadRange"] != 2 {
					t.Fatalf("topic error preceded own read writes: %v", steps)
				}
			default:
				feedRequire(t, err)
				feedRanges(t, pool, s, want) // own 7 read; unseen intervening messages unread
				var floors int
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM topic_read_floor WHERE organization_id=$1 AND member_id=$2", s.OrganizationID, s.MemberID).Scan(&floors))
				if floors != 0 {
					t.Fatalf("posting wrote %d topic floors; want none (decision 5)", floors)
				}
				if topic != nil {
					counts := trace.Counts()
					if counts != (platform.QueryCounts{Queries: 13, Begins: 1, Commits: 1}) {
						t.Fatalf("topic posting statements = %+v; want 13 queries, one begin/commit", counts)
					}
					wantSteps := strings.Fields(`NextEventSeq GetDefaultTopic GetTopic InsertMessage
						LastChannelMessageBefore LockChannelRead
						RangePredecessor DeleteRangeNeighbours InsertReadRange
						RangePredecessor DeleteRangeNeighbours InsertReadRange InsertEvent`)
					if !slices.Equal(steps, wantSteps) {
						t.Fatalf("topic posting order = %v; want %v", steps, wantSteps)
					}
				}
			}
		})
	}
}

func TestReadBoundsScope(t *testing.T) {
	h := newReadOracle(t)
	scope := h.scope(readOracleStep{})
	// Snapshot all three tables outside the author's URL scope, including
	// another member, channel and organisation, even when they have no rows.
	before := readOracleRows(t, h.pool, nil, false, scope)
	cursor := int64(660)
	posting := conversationpg.NewPagePosting(h.pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
	_, err := posting.Send(t.Context(), h.members[0], scope.ChannelID, conversation.Send{Body: "own", Cursor: &cursor})
	feedRequire(t, err)
	for _, violation := range readOracleCheckRows(before, readOracleRows(t, h.pool, nil, false, scope), "posting outside URL scope") {
		t.Error(violation)
	}
}
