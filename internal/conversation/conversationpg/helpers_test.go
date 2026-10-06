package conversationpg_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// eventKinds gives readers the same publisher registrations as cmd/ribbitto.
func eventKinds() realtime.Kinds {
	kinds := conversationpg.EventKinds()
	for kind, router := range orgpg.EventKinds() {
		kinds[kind] = router
	}
	return kinds
}

// membership is the posting member's membership; posting takes nothing else.
func membership(organizationID, memberID kernel.ID) org.Membership {
	return org.Membership{Organization: org.Organization{ID: organizationID}, Member: org.Member{ID: memberID}}
}

// eventSequence and appendEvents bind org's sequence and realtime's appender
// to posting's and branching's transaction, as cmd/ribbitto does.
func eventSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

func appendEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// newPosting builds posting on org's sequence and realtime's appender, as
// cmd/ribbitto does, without notifications.
func newPosting(pool *pgxpool.Pool) *conversation.Posting {
	return conversationpg.NewPosting(pool, eventSequence, appendEvents, nil)
}

// assertEventLog takes the scenario's boundary: compared with the boundary it
// reads, a boundary raised over a lost row would still balance the count.
func assertEventLog(t *testing.T, pool *pgxpool.Pool, org kernel.ID, wantBoundary, wantSeq int64) {
	t.Helper()
	var seq, boundary, count, valid int64
	requireNoError(t, pool.QueryRow(t.Context(), `
		SELECT o.event_seq, o.event_log_boundary_seq, count(e.seq),
		 count(e.seq) FILTER (WHERE e.seq > o.event_log_boundary_seq AND e.seq <= o.event_seq
		  AND e.audience_member_id IS NULL AND e.created_at IS NOT NULL AND (
		   (e.kind = 'message.posted' AND EXISTS (SELECT FROM message m
		    WHERE m.organization_id = o.id AND m.event_seq = e.seq
		    AND e.data = jsonb_build_object('channel_id', m.channel_id, 'message_id', m.id, 'topic_id', m.topic_id))) OR
		   (e.kind = 'member.joined' AND EXISTS (SELECT FROM member m
		    WHERE m.organization_id = o.id AND m.joined_event_seq = e.seq
		    AND e.data = jsonb_build_object('member_id', m.id)))))
		FROM organization o LEFT JOIN event_log e ON e.organization_id = o.id
		WHERE o.id = $1 GROUP BY o.id`, org).Scan(&seq, &boundary, &count, &valid))
	// The primary key makes count == interval length prove there are no gaps.
	if seq != wantSeq || boundary != wantBoundary || count != seq-boundary || valid != count {
		t.Fatalf("event log: seq=%d boundary=%d rows=%d valid=%d; want seq=%d boundary=%d", seq, boundary, count, valid, wantSeq, wantBoundary)
	}
}

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// lookupAccounts adapts identity's directory to the reader's consumer interface.
func lookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// eventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func eventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }
