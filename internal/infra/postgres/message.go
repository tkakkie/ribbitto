package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// MessageStore persists messages using a pool or a caller-owned transaction.
type MessageStore struct{ queries *sqlcgen.Queries }

// NewMessageStore returns a store using db.
func NewMessageStore(db sqlcgen.DBTX) *MessageStore {
	return &MessageStore{queries: sqlcgen.New(db)}
}

// InsertMessage stores a validated body at a sequence allocated by the caller's transaction.
func (s *MessageStore) InsertMessage(ctx context.Context, organizationID, channelID, memberID domain.ID, body string, eventSeq int64) (domain.Message, error) {
	row, err := s.queries.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true},
		MemberID: pgtype.UUID{Bytes: memberID, Valid: true}, Body: body, EventSeq: eventSeq,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("inserting message: %w", err)
	}
	return messageFromRow(row), nil
}

// ListMessagesBefore returns newest first; nil beforeEventSeq reads the latest page.
func (s *MessageStore) ListMessagesBefore(ctx context.Context, organizationID, channelID domain.ID, beforeEventSeq *int64, limit int32) ([]domain.Message, error) {
	var before pgtype.Int8
	if beforeEventSeq != nil {
		before = pgtype.Int8{Int64: *beforeEventSeq, Valid: true}
	}
	rows, err := s.queries.ListMessagesBefore(ctx, sqlcgen.ListMessagesBeforeParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, BeforeEventSeq: before, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	messages := make([]domain.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messageFromRow(row))
	}
	return messages, nil
}

func messageFromRow(row sqlcgen.Message) domain.Message {
	return domain.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}
}
