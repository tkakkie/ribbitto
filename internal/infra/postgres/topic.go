package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// TopicStore persists topics using a pool or a caller-owned transaction.
type TopicStore struct{ queries *sqlcgen.Queries }

// NewTopicStore returns a store using db.
func NewTopicStore(db sqlcgen.DBTX) *TopicStore {
	return &TopicStore{queries: sqlcgen.New(db)}
}

// CreateTopic inserts a named topic with an already validated name.
func (s *TopicStore) CreateTopic(ctx context.Context, organizationID, channelID domain.ID, name string) (domain.Topic, error) {
	return s.create(ctx, organizationID, channelID, pgtype.Text{String: name, Valid: true}, false)
}

// CreateDefaultTopic inserts the channel's default topic. The database
// refuses a second one for the same channel.
func (s *TopicStore) CreateDefaultTopic(ctx context.Context, organizationID, channelID domain.ID) (domain.Topic, error) {
	return s.create(ctx, organizationID, channelID, pgtype.Text{}, true)
}

func (s *TopicStore) create(ctx context.Context, organizationID, channelID domain.ID, name pgtype.Text, isDefault bool) (domain.Topic, error) {
	row, err := s.queries.CreateTopic(ctx, sqlcgen.CreateTopicParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true},
		Name:           name,
		IsDefault:      isDefault,
	})
	var pgErr *pgconn.PgError
	switch {
	// The unique index, not a lookup first, decides between concurrent creators.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "topic_name_idx":
		return domain.Topic{}, topic.ErrNameTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "topic_name_check":
		return domain.Topic{}, fmt.Errorf("%w: %w", topic.ErrInvalidName, err)
	case err != nil:
		return domain.Topic{}, fmt.Errorf("creating topic: %w", err)
	}
	return topicFromRow(row), nil
}

// GetTopic looks up an ID within the organisation and channel.
func (s *TopicStore) GetTopic(ctx context.Context, organizationID, channelID, id domain.ID) (domain.Topic, error) {
	row, err := s.queries.GetTopic(ctx, sqlcgen.GetTopicParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true},
		ID:             pgtype.UUID{Bytes: id, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Topic{}, topic.ErrNotFound
	}
	if err != nil {
		return domain.Topic{}, fmt.Errorf("getting topic: %w", err)
	}
	return topicFromRow(row), nil
}

// GetDefaultTopic returns the channel's default topic.
func (s *TopicStore) GetDefaultTopic(ctx context.Context, organizationID, channelID domain.ID) (domain.Topic, error) {
	row, err := s.queries.GetDefaultTopic(ctx, sqlcgen.GetDefaultTopicParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Topic{}, topic.ErrNotFound
	}
	if err != nil {
		return domain.Topic{}, fmt.Errorf("getting default topic: %w", err)
	}
	return topicFromRow(row), nil
}

// ListTopics returns at most limit topics of the channel, the default first,
// then in creation order (UUIDv7 ids sort by creation time).
func (s *TopicStore) ListTopics(ctx context.Context, organizationID, channelID domain.ID, limit int) ([]domain.Topic, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("listing topics: limit %d out of range", limit)
	}
	rows, err := s.queries.ListTopics(ctx, sqlcgen.ListTopicsParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true},
		Limit:          int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("listing topics: %w", err)
	}
	topics := make([]domain.Topic, 0, len(rows))
	for _, row := range rows {
		topics = append(topics, topicFromRow(row))
	}
	return topics, nil
}

func topicFromRow(row sqlcgen.Topic) domain.Topic {
	return domain.Topic{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, Name: row.Name.String, IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time}
}

// LookupTopics implements topic.Directory without per-message queries.
func (s *TopicStore) LookupTopics(ctx context.Context, organizationID, channelID domain.ID, ids []domain.ID) (map[domain.ID]domain.Topic, error) {
	rows, err := s.queries.LookupTopics(ctx, sqlcgen.LookupTopicsParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ChannelID:      pgtype.UUID{Bytes: channelID, Valid: true}, TopicIds: uuidArray(ids),
	})
	if err != nil {
		return nil, fmt.Errorf("looking up topics: %w", err)
	}
	result := make(map[domain.ID]domain.Topic, len(rows))
	for _, row := range rows {
		result[row.ID.Bytes] = topicFromRow(row)
	}
	return result, nil
}
