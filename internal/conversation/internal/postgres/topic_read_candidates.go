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
func TopicReadCandidatesIn(tx platform.Tx) conversation.TopicReadCandidates {
	return topicReadCandidates{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

type topicReadCandidates struct{ queries *sqlcgen.Queries }

func (t topicReadCandidates) Ranges(ctx context.Context, organizationID, channelID, topicID kernel.ID, cursor, prefix, floor int64, readSet []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	ranges := make(pgtype.Multirange[pgtype.Range[pgtype.Int8]], len(readSet))
	for i, r := range readSet {
		ranges[i] = pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: r.Lo, Valid: true}, Upper: pgtype.Int8{Int64: r.Hi, Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}
	}
	rows, err := t.queries.TopicUnreadRangeBounds(ctx, sqlcgen.TopicUnreadRangeBoundsParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicID: uuid(topicID),
		Cursor: cursor, PrefixBefore: prefix - 1, Floor: floor, ReadSet: ranges,
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
