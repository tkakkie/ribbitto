package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type failingNoticeEvents struct{ conversation.EventAppender }

func (e failingNoticeEvents) Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	if kind == conversation.KindPosted {
		return errBranchNoticeAppend
	}
	return e.EventAppender.Append(ctx, organizationID, seq, kind, audience, data)
}

var errBranchNoticeAppend = errors.New("notice append failed")

func TestBranchNoticeRead(t *testing.T) {
	t.Parallel()
	for _, exists := range []bool{false, true} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t rollback=%t", exists, rollback), func(t *testing.T) {
				pool, other := feedFixture(t) // channel messages 2–6
				account := identitytest.Account(t, pool, "brancher@example.org", "Brancher")
				brancherScope := other
				brancherScope.MemberID = orgtest.Member(t, pool, other.OrganizationID, account, org.RoleMember, "brancher", 5)
				m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: account}, "feed")
				feedRequire(t, err)
				before := []unread.Range{}
				if exists {
					before = []unread.Range{{Lo: 0, Hi: 6}}
					feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
						return unreadpg.WriterIn(tx).Merge(t.Context(), brancherScope, 5, unread.Range{Lo: 0, Hi: 6})
					}))
				}
				feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
					return unreadpg.WriterIn(tx).Merge(t.Context(), other, 1, unread.Range{Lo: 0, Hi: 3})
				}))
				var source, readMessage, unreadMessage kernel.ID
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", other.ChannelID).Scan(&source))
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT id FROM message WHERE organization_id=$1 AND event_seq=2", other.OrganizationID).Scan(&readMessage))
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT id FROM message WHERE organization_id=$1 AND event_seq=6", other.OrganizationID).Scan(&unreadMessage))
				events := postingEvents
				if rollback {
					events = func(tx platform.Tx) conversation.EventAppender { return failingNoticeEvents{postingEvents(tx)} }
				}
				brancher := conversationpg.NewBrancher(pool, postingSequence, events, branchReads, nil)
				destination, err := brancher.Branch(t.Context(), m, other.ChannelID, conversation.Branch{
					Messages: []kernel.ID{readMessage, unreadMessage}, From: source, NewName: "design",
				}, func(conversation.Topic) string { return "moved" })
				if rollback {
					if !errors.Is(err, errBranchNoticeAppend) {
						t.Fatal(err)
					}
					feedRanges(t, pool, brancherScope, before)
				} else {
					feedRequire(t, err)
					// Exact ranges preserve every existing message's read status, including
					// the moved read 2 and unread 6; only notice 8 becomes newly read.
					feedRanges(t, pool, brancherScope, []unread.Range{{Lo: 0, Hi: 6}, {Lo: 7, Hi: 9}})
					var noticeSeq int64
					feedRequire(t, pool.QueryRow(t.Context(), "SELECT event_seq FROM message WHERE organization_id=$1 AND channel_id=$2 AND topic_id=$3 AND member_id=$4", other.OrganizationID, other.ChannelID, source, m.Member.ID).Scan(&noticeSeq))
					if noticeSeq != 8 {
						t.Fatalf("notice sequence=%d, want 8", noticeSeq)
					}
					var moved int
					feedRequire(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE topic_id=$1 AND id=ANY($2::uuid[])", destination.ID, []kernel.ID{readMessage, unreadMessage}).Scan(&moved))
					if moved != 2 {
						t.Fatalf("moved=%d", moved)
					}
				}
				// The other member keeps read 2, unread 6 and an unread notice.
				feedRanges(t, pool, other, []unread.Range{{Lo: 0, Hi: 3}})
				var locks int
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3", brancherScope.OrganizationID, brancherScope.ChannelID, brancherScope.MemberID).Scan(&locks))
				if want := !rollback || exists; (locks == 1) != want {
					t.Fatalf("lock rows=%d, want exists=%t", locks, want)
				}
			})
		}
	}
}
