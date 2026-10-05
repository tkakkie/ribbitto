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
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// ReadStoreIn binds conversation's reads to the caller's snapshot without
// managing its lifecycle.
func ReadStoreIn(snapshot platform.Snapshot) ReadStore {
	return ReadStore{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

// ReadStore implements conversation.ReadStore. It holds only queries and has
// no write method.
type ReadStore struct{ queries *sqlcgen.Queries }

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
