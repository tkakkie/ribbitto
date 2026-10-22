package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// MessageSequencesIn binds channel message bounds to the caller's transaction.
func MessageSequencesIn(tx platform.Tx) *MessageSequences {
	return &MessageSequences{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// MessageSequences queries channel message bounds and existence in a caller-owned transaction.
type MessageSequences struct{ queries *sqlcgen.Queries }

// FirstMessageAfter returns the first sequence strictly above cursor, or zero.
func (m MessageSequences) FirstMessageAfter(ctx context.Context, organizationID, channelID kernel.ID, cursor int64) (int64, error) {
	seq, err := m.queries.FirstChannelMessageAfter(ctx, sqlcgen.FirstChannelMessageAfterParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), EventSeq: cursor,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("finding next channel message: %w", err)
	}
	return seq, nil
}

// HasMessages probes parallel half-open bounds in one scoped statement.
func (m MessageSequences) HasMessages(ctx context.Context, organizationID, channelID kernel.ID, los, his []int64) (bool, error) {
	if len(los) != len(his) {
		return false, fmt.Errorf("checking channel messages: unequal bound lengths")
	}
	if len(los) == 0 {
		return false, fmt.Errorf("checking channel messages: empty bounds")
	}
	lowest, highest := los[0], his[0]
	ranges := make(pgtype.Multirange[pgtype.Range[pgtype.Int8]], len(los))
	// Scan all inputs so bounds do not depend on their order or MergeCovered's
	// output order. They only narrow the index range; multirange containment
	// still decides membership.
	for i, lo := range los {
		lowest = min(lowest, lo)
		highest = max(highest, his[i])
		ranges[i] = pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: lo, Valid: true}, Upper: pgtype.Int8{Int64: his[i], Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}
	}
	exists, err := m.queries.ChannelHasMessages(ctx, sqlcgen.ChannelHasMessagesParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), Lowest: lowest, Highest: highest, CoveredSet: ranges})
	if err != nil {
		return false, fmt.Errorf("checking channel messages: %w", err)
	}
	return exists, nil
}
