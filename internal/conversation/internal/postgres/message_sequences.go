package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// MessageSequencesIn binds channel message bounds to the caller's transaction.
func MessageSequencesIn(tx platform.Tx) conversation.MessageSequences {
	return messageSequences{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

type messageSequences struct{ queries *sqlcgen.Queries }

func (m messageSequences) FirstMessageAfter(ctx context.Context, organizationID, channelID kernel.ID, cursor int64) (int64, error) {
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
