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

// WriterIn binds posting's and branching's writes to the caller's transaction.
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
	posted, err := w.insert(ctx, organizationID, channelID, topicID, memberID, body, eventSeq)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_channel_id_fkey":
		return conversation.Message{}, conversation.ErrChannelNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_organization_id_member_id_fkey":
		return conversation.Message{}, org.ErrNotFound
	case err != nil:
		return conversation.Message{}, err
	}
	return posted, nil
}

// InsertNotice inserts branching's notice. It deliberately maps neither
// foreign key: branching never translated them, so its failure stays a
// server error rather than a 404 (R2 on #502).
func (w Writer) InsertNotice(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, eventSeq int64) (conversation.Message, error) {
	return w.insert(ctx, organizationID, channelID, topicID, memberID, body, eventSeq)
}

// insert runs the query posting and branching share and wraps any error.
func (w Writer) insert(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, eventSeq int64) (conversation.Message, error) {
	row, err := w.queries.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, TopicID: pgtype.UUID{Bytes: topicID, Valid: true},
		MemberID: pgtype.UUID{Bytes: memberID, Valid: true}, Body: body, EventSeq: eventSeq,
	})
	if err != nil {
		return conversation.Message{}, fmt.Errorf("inserting message: %w", err)
	}
	return conversation.Message{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, ChannelID: row.ChannelID.Bytes, TopicID: row.TopicID.Bytes, MemberID: row.MemberID.Bytes, Body: row.Body, EventSeq: row.EventSeq, CreatedAt: row.CreatedAt.Time}, nil
}

// CreateTopic inserts a named topic with an already validated name.
func (w Writer) CreateTopic(ctx context.Context, organizationID, channelID kernel.ID, name string) (conversation.Topic, error) {
	row, err := w.queries.CreateTopic(ctx, sqlcgen.CreateTopicParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, Name: pgtype.Text{String: name, Valid: true},
	})
	var pgErr *pgconn.PgError
	switch {
	// The unique index, not a lookup first, decides between concurrent creators.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "topic_name_idx":
		return conversation.Topic{}, conversation.ErrTopicNameTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "topic_name_check":
		return conversation.Topic{}, fmt.Errorf("%w: %w", conversation.ErrInvalidTopicName, err)
	case err != nil:
		return conversation.Topic{}, fmt.Errorf("creating topic: %w", err)
	}
	return topicFromRow(row), nil
}

// MoveMessages moves the messages still in the source topic, within the
// organisation and channel, and returns how many moved.
func (w Writer) MoveMessages(ctx context.Context, organizationID, channelID, fromTopicID, toTopicID kernel.ID, messageIDs []kernel.ID) (int64, error) {
	ids := make([]pgtype.UUID, len(messageIDs))
	for i, id := range messageIDs {
		ids[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	moved, err := w.queries.MoveMessages(ctx, sqlcgen.MoveMessagesParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true},
		FromTopicID: pgtype.UUID{Bytes: fromTopicID, Valid: true}, ToTopicID: pgtype.UUID{Bytes: toTopicID, Valid: true}, MessageIds: ids,
	})
	if err != nil {
		return 0, fmt.Errorf("moving messages: %w", err)
	}
	return moved, nil
}
