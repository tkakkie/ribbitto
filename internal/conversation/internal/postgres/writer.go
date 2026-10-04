package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// WriterIn binds posting's writes to the caller's transaction.
func WriterIn(tx platform.Tx) Writer {
	db := pgxbridge.Tx(tx)
	return Writer{queries: sqlcgen.New(db), topics: NewTopicStore(db)}
}

// Writer implements conversation.Writer. It translates constraint names into
// conversation's and org's errors, so the use cases never see pgconn.
type Writer struct {
	queries *sqlcgen.Queries
	topics  *TopicStore
}

// GetDefaultTopic returns the channel's default topic in the organisation.
func (w Writer) GetDefaultTopic(ctx context.Context, organizationID, channelID kernel.ID) (conversation.Topic, error) {
	row, err := w.queries.GetDefaultTopic(ctx, sqlcgen.GetDefaultTopicParams{OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Topic{}, conversation.ErrTopicNotFound
	}
	if err != nil {
		return conversation.Topic{}, fmt.Errorf("getting default topic: %w", err)
	}
	return topicFromRow(row), nil
}

// GetTopic looks up an ID within the organisation and channel, as TopicStore does.
func (w Writer) GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (conversation.Topic, error) {
	return w.topics.GetTopic(ctx, organizationID, channelID, id)
}

// InsertMessage inserts posting's message. The composite foreign keys, not a
// lookup first, keep a message inside its organisation: another
// organisation's channel or member fails here. The caller resolves topicID
// in the same transaction first, so a topic outside the channel never
// reaches the insert and message_topic_fkey stays unmapped.
func (w Writer) InsertMessage(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, eventSeq int64) (conversation.Message, error) {
	row, err := w.queries.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, TopicID: pgtype.UUID{Bytes: topicID, Valid: true},
		MemberID: pgtype.UUID{Bytes: memberID, Valid: true}, Body: body, EventSeq: eventSeq,
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_channel_id_fkey":
		return conversation.Message{}, conversation.ErrChannelNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_member_id_fkey":
		return conversation.Message{}, org.ErrNotFound
	case err != nil:
		return conversation.Message{}, fmt.Errorf("inserting message: %w", err)
	}
	return conversation.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, TopicID: row.TopicID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}, nil
}
