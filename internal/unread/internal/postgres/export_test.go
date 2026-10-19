package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// MergeBeforeCoverageForTest is the union from c283544^, with identical SQL and locks,
// but without #834's sorting/subtraction. Only valid fixture inputs reach it.
func MergeBeforeCoverageForTest(ctx context.Context, w *Writer, s unread.Scope, joined int64, r conversation.SequenceRange) error {
	p := scopeParams(s)
	if err := w.lock(ctx, p); err != nil {
		return err
	}
	for _, added := range []conversation.SequenceRange{{Lo: 0, Hi: joined + 1}, r} {
		lower := added.Lo
		previous, err := w.queries.RangePredecessor(ctx, sqlcgen.RangePredecessorParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, Lo: added.Lo})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// No predecessor means the same lower bound as the production implementation.
		if err == nil {
			lower = previous.Lo
		}
		removed, err := w.queries.DeleteRangeNeighbours(ctx, sqlcgen.DeleteRangeNeighboursParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, LowerLo: lower, Lo: added.Lo, Hi: added.Hi})
		if err != nil {
			return err
		}
		for _, r := range removed {
			added.Lo = min(added.Lo, r.Lo)
			added.Hi = max(added.Hi, r.Hi)
		}
		if err := w.queries.InsertReadRange(ctx, sqlcgen.InsertReadRangeParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, Lo: added.Lo, Hi: added.Hi}); err != nil {
			return err
		}
	}
	return nil
}
