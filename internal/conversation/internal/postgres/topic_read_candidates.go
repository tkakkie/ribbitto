package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// TopicReadCandidatesIn binds topic unread bounds to the caller's transaction.
func TopicReadCandidatesIn(tx platform.Tx) *TopicReadCandidates {
	return &TopicReadCandidates{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// TopicReadCandidates queries unread topic bounds in a caller-owned transaction.
type TopicReadCandidates struct{ queries *sqlcgen.Queries }

// Ranges returns whole-channel neighbour bounds for the topic's unread messages.
func (t TopicReadCandidates) Ranges(ctx context.Context, organizationID, channelID, topicID kernel.ID, cursor, prefix, floor int64, readSet []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	return t.ShownRanges(ctx, organizationID, channelID, topicID, cursor, 0, cursor, prefix, floor, readSet)
}

// ShownRanges limits the topic's unread candidates to [from, through].
func (t TopicReadCandidates) ShownRanges(ctx context.Context, organizationID, channelID, topicID kernel.ID, cursor, from, through, prefix, floor int64, readSet []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	rows, err := t.queries.TopicUnreadRangeBounds(ctx, sqlcgen.TopicUnreadRangeBoundsParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicID: uuid(topicID),
		Cursor: cursor, RunFrom: from, RunThrough: through, PrefixBefore: prefix - 1, Floor: floor, ReadSet: topicReadSet(readSet),
	})
	if err != nil {
		return nil, fmt.Errorf("finding topic unread bounds: %w", err)
	}
	result := make([]conversation.SequenceRange, len(rows))
	for i, r := range rows {
		result[i] = conversation.SequenceRange{Lo: r.Lo, Hi: r.Hi}
	}
	return result, nil
}

// RunFloor returns the exclusive safe floor bound, capped at runEnd.
// The read set and floor must come from locked preparation in this transaction.
func (t TopicReadCandidates) RunFloor(ctx context.Context, organizationID, channelID, topicID kernel.ID, from, runEnd, prefix, floor int64, readSet []conversation.SequenceRange) (int64, error) {
	blocking, err := t.queries.TopicRunFloor(ctx, sqlcgen.TopicRunFloorParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicID: uuid(topicID),
		RunFrom: from, RunEnd: runEnd, PrefixBefore: prefix - 1, Floor: floor, ReadSet: topicReadSet(readSet),
	})
	if err != nil {
		return 0, fmt.Errorf("finding topic run floor: %w", err)
	}
	return blocking, nil
}

func topicReadSet(readSet []conversation.SequenceRange) pgtype.Multirange[pgtype.Range[pgtype.Int8]] {
	ranges := make(pgtype.Multirange[pgtype.Range[pgtype.Int8]], len(readSet))
	for i, r := range readSet {
		ranges[i] = pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: r.Lo, Valid: true}, Upper: pgtype.Int8{Int64: r.Hi, Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}
	}
	return ranges
}
