package compose

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// BranchReads adapts unread's range writer to conversation's consumer interface.
func BranchReads(tx platform.Tx) conversation.ReadRangeWriter {
	return branchReadWriter{ranges: unreadpg.WriterIn(tx)}
}

type branchReadWriter struct{ ranges unread.RangeWriter }

func (w branchReadWriter) Merge(ctx context.Context, organizationID, channelID, memberID kernel.ID, joined, lo, hi int64) error {
	return w.ranges.Merge(ctx, unread.Scope{OrganizationID: organizationID, ChannelID: channelID, MemberID: memberID}, joined, conversation.SequenceRange{Lo: lo, Hi: hi})
}
