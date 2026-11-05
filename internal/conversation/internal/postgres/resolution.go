package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// SetTopicConclusion writes the conclusion checked under the organisation lock.
func (w Writer) SetTopicConclusion(ctx context.Context, organizationID, channelID, topicID, messageID kernel.ID) error {
	n, err := w.queries.SetTopicConclusion(ctx, sqlcgen.SetTopicConclusionParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), ID: uuid(topicID),
		ConclusionMessageID: pgtype.UUID{Bytes: messageID, Valid: messageID != (kernel.ID{})},
	})
	if err != nil {
		return fmt.Errorf("setting topic conclusion: %w", err)
	}
	if n == 0 {
		return conversation.ErrTopicNotFound
	}
	return nil
}
