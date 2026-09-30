package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
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

// GetMessage returns the message at the scoped event sequence, or message.ErrNotFound.
func (s *MessageStore) GetMessage(ctx context.Context, organizationID, channelID domain.ID, eventSeq int64) (domain.Message, error) {
	row, err := s.queries.GetMessage(ctx, sqlcgen.GetMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, EventSeq: eventSeq,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, message.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, fmt.Errorf("finding message: %w", err)
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

// PostingStore implements message.Store: it owns the posting transaction.
type PostingStore struct{ pool *pgxpool.Pool }

// NewPostingStore returns a PostingStore on pool.
func NewPostingStore(pool *pgxpool.Pool) *PostingStore { return &PostingStore{pool: pool} }

// Post takes the next event_seq first — locking the organisation's row, so
// sequence order is commit order — then inserts the message and event.
// Any failure rolls everything back, so no sequence value is lost. Listed
// exceptions: advances org's event_seq and writes realtime's event_log.
func (s *PostingStore) Post(ctx context.Context, organizationID, channelID, memberID domain.ID, body string) (domain.Message, error) {
	var posted domain.Message
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		seq, err := sqlcgen.New(tx).NextEventSeq(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
		if err != nil {
			return err
		}
		posted, err = NewMessageStore(tx).InsertMessage(ctx, organizationID, channelID, memberID, body, seq)
		if err != nil {
			return err
		}
		return sqlcgen.New(tx).InsertMessageEvent(ctx, sqlcgen.InsertMessageEventParams{
			OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Seq: seq, Kind: string(domain.EventMessagePosted),
			ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, MessageID: pgtype.UUID{Bytes: posted.ID, Valid: true},
		})
	})
	var pgErr *pgconn.PgError
	switch {
	// The composite foreign keys, not a lookup first, keep a message inside
	// its organisation: another organisation's channel or member fails here.
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_channel_id_fkey":
		return domain.Message{}, channel.ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_member_id_fkey":
		return domain.Message{}, authz.ErrNotFound
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Message{}, authz.ErrNotFound // the organisation itself is gone
	case err != nil:
		return domain.Message{}, fmt.Errorf("posting message: %w", err)
	}
	return posted, nil
}
