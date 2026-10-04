package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// TopicStore reads topics using a pool or a caller-owned transaction.
type TopicStore struct{ queries *sqlcgen.Queries }

// NewTopicStore returns a store using db.
func NewTopicStore(db sqlcgen.DBTX) *TopicStore {
	return &TopicStore{queries: sqlcgen.New(db)}
}

// GetTopic looks up an ID within the organisation and channel. A topic of
// another channel or organisation is ErrTopicNotFound, like a missing one.
func (s *TopicStore) GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	row, err := s.queries.GetTopic(ctx, sqlcgen.GetTopicParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true},
		ID:             pgtype.UUID{Bytes: id, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Topic{}, conversation.ErrTopicNotFound
	}
	if err != nil {
		return conversation.Topic{}, fmt.Errorf("getting topic: %w", err)
	}
	return topicFromRow(row), nil
}

func topicFromRow(row sqlcgen.Topic) conversation.Topic {
	return conversation.Topic{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, Name: row.Name.String, IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time}
}
