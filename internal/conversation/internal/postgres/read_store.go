package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// ReadStoreIn binds conversation's reads to the caller's snapshot without
// managing its lifecycle.
func ReadStoreIn(snapshot platform.Snapshot) ReadStore {
	db := pgxbridge.Snapshot(snapshot)
	return ReadStore{channels: NewChannelStore(db), topics: NewTopicStore(db), queries: sqlcgen.New(db)}
}

// ReadStore implements conversation.ReadStore. It delegates to the channel
// and topic stores rather than embedding them, so it never gains their write
// methods.
type ReadStore struct {
	channels *ChannelStore
	topics   *TopicStore
	queries  *sqlcgen.Queries
}

// GetChannel looks up an ID within the organisation.
func (s ReadStore) GetChannel(ctx context.Context, organizationID, id kernel.ID) (conversation.Channel, error) {
	return s.channels.GetChannel(ctx, organizationID, id)
}

// ListChannels returns the organisation's channels ordered by name and ID.
func (s ReadStore) ListChannels(ctx context.Context, organizationID kernel.ID) ([]conversation.Channel, error) {
	return s.channels.ListChannels(ctx, organizationID)
}

// GetTopic looks up an ID within the organisation and channel.
func (s ReadStore) GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	return s.topics.GetTopic(ctx, organizationID, channelID, id)
}

// ListTopics returns at most limit topics of the channel, the default first,
// then by name (case-insensitive), with ID as a tie-breaker.
func (s ReadStore) ListTopics(ctx context.Context, organizationID, channelID kernel.ID, limit int) ([]conversation.Topic, error) {
	// The query takes an int32; a larger limit would wrap.
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("listing topics: limit %d out of range", limit)
	}
	rows, err := s.queries.ListTopics(ctx, sqlcgen.ListTopicsParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("listing topics: %w", err)
	}
	topics := make([]conversation.Topic, 0, len(rows))
	for _, row := range rows {
		topics = append(topics, topicFromRow(row))
	}
	return topics, nil
}

// LookupTopics implements conversation.ReadStore's topic batch without
// per-message queries. Missing and out-of-scope IDs are omitted.
func (s ReadStore) LookupTopics(ctx context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	rows, err := s.queries.LookupTopics(ctx, sqlcgen.LookupTopicsParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicIds: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("looking up topics: %w", err)
	}
	result := make(map[kernel.ID]conversation.Topic, len(rows))
	for _, row := range rows {
		result[row.ID.Bytes] = topicFromRow(row)
	}
	return result, nil
}

// GetMessage returns the message at the scoped event sequence, or conversation.ErrMessageNotFound.
func (s ReadStore) GetMessage(ctx context.Context, organizationID, channelID kernel.ID, eventSeq int64) (conversation.Message, error) {
	row, err := s.queries.GetMessage(ctx, sqlcgen.GetMessageParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), EventSeq: eventSeq})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Message{}, conversation.ErrMessageNotFound
	}
	if err != nil {
		return conversation.Message{}, fmt.Errorf("finding message: %w", err)
	}
	return messageFromRow(row), nil
}

// ListMessagesBefore returns newest first; nil beforeEventSeq reads the latest page.
// A nil topicID includes every topic in the channel.
func (s ReadStore) ListMessagesBefore(ctx context.Context, organizationID, channelID kernel.ID, topicID *kernel.ID, beforeEventSeq *int64, limit int32) ([]conversation.Message, error) {
	params := sqlcgen.ListMessagesBeforeParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), Limit: limit}
	if topicID != nil {
		params.TopicID = uuid(*topicID)
	}
	if beforeEventSeq != nil {
		params.BeforeEventSeq = pgtype.Int8{Int64: *beforeEventSeq, Valid: true}
	}
	rows, err := s.queries.ListMessagesBefore(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	return messagesFromRows(rows), nil
}

// GetMessages returns the requested messages of the channel, newest first;
// missing and out-of-scope IDs are omitted.
func (s ReadStore) GetMessages(ctx context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	rows, err := s.queries.GetMessages(ctx, sqlcgen.GetMessagesParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), MessageIds: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("finding messages: %w", err)
	}
	return messagesFromRows(rows), nil
}

func messagesFromRows(rows []sqlcgen.Message) []conversation.Message {
	messages := make([]conversation.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messageFromRow(row))
	}
	return messages
}

func messageFromRow(row sqlcgen.Message) conversation.Message {
	return conversation.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, TopicID: row.TopicID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}
}

func uuid(id kernel.ID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func uuids(ids []kernel.ID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i] = uuid(id)
	}
	return result
}
