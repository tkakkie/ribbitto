package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
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
func (s *MessageStore) InsertMessage(ctx context.Context, organizationID, channelID, topicID, memberID domain.ID, body string, eventSeq int64) (domain.Message, error) {
	row, err := s.queries.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, TopicID: pgtype.UUID{Bytes: topicID, Valid: true},
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
// A nil topicID includes every topic in the channel.
func (s *MessageStore) ListMessagesBefore(ctx context.Context, organizationID, channelID domain.ID, topicID *domain.ID, beforeEventSeq *int64, limit int32) ([]domain.Message, error) {
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
	messages := make([]domain.Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, messageFromRow(row))
	}
	return messages, nil
}

func messageFromRow(row sqlcgen.Message) domain.Message {
	return domain.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, TopicID: row.TopicID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}
}

// PostingStore implements message.Store: it owns the posting transaction.
type PostingStore struct {
	pool      *pgxpool.Pool
	sequences EventSequenceIn
	events    EventAppenderIn
}

// NewPostingStore returns a PostingStore on pool that takes org's sequence
// through sequences and appends events through events.
func NewPostingStore(pool *pgxpool.Pool, sequences EventSequenceIn, events EventAppenderIn) *PostingStore {
	return &PostingStore{pool: pool, sequences: sequences, events: events}
}

// Post takes the next event_seq first — locking the organisation's row, so
// sequence order is commit order — then inserts the message and event.
// Any failure rolls everything back, so no sequence value is lost. Listed
// exceptions: advances org's event_seq and writes realtime's event_log.
func (s *PostingStore) Post(ctx context.Context, organizationID, channelID, memberID domain.ID, body string) (domain.Message, error) {
	return s.PostToTopic(ctx, organizationID, channelID, memberID, nil, body)
}

// PostToTopic posts into the scoped topic, or the default when topicID is nil.
func (s *PostingStore) PostToTopic(ctx context.Context, organizationID, channelID, memberID domain.ID, topicID *domain.ID, body string) (domain.Message, error) {
	var posted domain.Message
	err := platform.InTx(ctx, s.pool, func(platformTx platform.Tx) error {
		tx := pgxbridge.Tx(platformTx)
		seq, err := s.sequences(platformTx).NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		// A message posted without a topic goes to the channel's default
		// topic (decision 21), read through topic's API. A channel always
		// has one, so none means the channel is not in this organisation.
		defaultTopic, err := NewTopicStore(tx).GetDefaultTopic(ctx, organizationID, channelID)
		if errors.Is(err, topic.ErrNotFound) {
			return channel.ErrNotFound
		}
		if err != nil {
			return err
		}
		if topicID != nil {
			defaultTopic, err = NewTopicStore(tx).GetTopic(ctx, organizationID, channelID, *topicID)
			if err != nil {
				return err
			}
		}
		posted, err = NewMessageStore(tx).InsertMessage(ctx, organizationID, channelID, defaultTopic.ID, memberID, body, seq)
		if err != nil {
			return err
		}
		data := message.EncodePosted(channelID, posted.ID, posted.TopicID)
		return s.events(platformTx).Append(ctx, organizationID, seq, message.KindPosted, nil, data)
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, channel.ErrNotFound):
		return domain.Message{}, channel.ErrNotFound
	// The composite foreign keys, not a lookup first, keep a message inside
	// its organisation: another organisation's channel or member fails here.
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_channel_id_fkey":
		return domain.Message{}, channel.ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_member_id_fkey":
		return domain.Message{}, org.ErrNotFound
	case errors.Is(err, org.ErrNotFound):
		return domain.Message{}, org.ErrNotFound // the organisation itself is gone
	case err != nil:
		return domain.Message{}, fmt.Errorf("posting message: %w", err)
	}
	return posted, nil
}

// GetMessages reads only the requested IDs in the organisation and channel.
func (s *MessageStore) GetMessages(ctx context.Context, organizationID, channelID domain.ID, ids []domain.ID) ([]domain.Message, error) {
	rows, err := s.queries.GetMessages(ctx, sqlcgen.GetMessagesParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, MessageIds: uuidArray(ids),
	})
	if err != nil {
		return nil, fmt.Errorf("finding messages: %w", err)
	}
	messages := make([]domain.Message, 0, len(rows))
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
