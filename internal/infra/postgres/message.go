package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// MessageStore persists messages using a pool or a caller-owned transaction.
type MessageStore struct {
	queries *sqlcgen.Queries
}

// NewMessageStore returns a store using db.
func NewMessageStore(db sqlcgen.DBTX) *MessageStore {
	return &MessageStore{queries: sqlcgen.New(db)}
}

// InsertMessage stores a validated body in a topic of the channel at a
// sequence allocated by the caller's transaction.
func (s *MessageStore) InsertMessage(ctx context.Context, organizationID, channelID, topicID, memberID domain.ID, body string, eventSeq int64) (conversation.Message, error) {
	row, err := s.queries.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, TopicID: pgtype.UUID{Bytes: topicID, Valid: true},
		MemberID: pgtype.UUID{Bytes: memberID, Valid: true}, Body: body, EventSeq: eventSeq,
	})
	if err != nil {
		return conversation.Message{}, fmt.Errorf("inserting message: %w", err)
	}
	return messageFromRow(row), nil
}

// GetMessage returns the message at the scoped event sequence, or conversation.ErrMessageNotFound.
func (s *MessageStore) GetMessage(ctx context.Context, organizationID, channelID domain.ID, eventSeq int64) (conversation.Message, error) {
	row, err := s.queries.GetMessage(ctx, sqlcgen.GetMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, EventSeq: eventSeq,
	})
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
func (s *MessageStore) ListMessagesBefore(ctx context.Context, organizationID, channelID domain.ID, topicID *domain.ID, beforeEventSeq *int64, limit int32) ([]conversation.Message, error) {
	var selectedTopic pgtype.UUID
	if topicID != nil {
		selectedTopic = pgtype.UUID{Bytes: *topicID, Valid: true}
	}
	var before pgtype.Int8
	if beforeEventSeq != nil {
		before = pgtype.Int8{Int64: *beforeEventSeq, Valid: true}
	}
	rows, err := s.queries.ListMessagesBefore(ctx, sqlcgen.ListMessagesBeforeParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, BeforeEventSeq: before, Limit: limit, TopicID: selectedTopic,
	})
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	messages := make([]conversation.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messageFromRow(row))
	}
	return messages, nil
}

func messageFromRow(row sqlcgen.Message) conversation.Message {
	return conversation.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, TopicID: row.TopicID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}
}

// GetMessages reads only the requested IDs in the organisation and channel.
func (s *MessageStore) GetMessages(ctx context.Context, organizationID, channelID domain.ID, ids []domain.ID) ([]conversation.Message, error) {
	rows, err := s.queries.GetMessages(ctx, sqlcgen.GetMessagesParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, MessageIds: uuidArray(ids),
	})
	if err != nil {
		return nil, fmt.Errorf("finding messages: %w", err)
	}
	messages := make([]conversation.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messageFromRow(row))
	}
	return messages, nil
}

func uuidArray(ids []domain.ID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	return result
}
