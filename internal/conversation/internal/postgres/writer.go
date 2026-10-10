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
	return newWriter(pgxbridge.Tx(tx))
}

func newWriter(db sqlcgen.DBTX) Writer {
	return Writer{TopicStore: NewTopicStore(db), queries: sqlcgen.New(db)}
}

// Writer implements conversation.Writer. It translates constraint names into
// conversation's and org's errors, so the use cases never see pgconn.
type Writer struct {
	*TopicStore
	queries *sqlcgen.Queries
}

// GetDefaultTopic returns the channel's default topic in the organisation.
func (w Writer) GetDefaultTopic(ctx context.Context, organizationID, channelID kernel.ID) (conversation.Topic, error) {
	row, err := w.queries.GetDefaultTopic(ctx, sqlcgen.GetDefaultTopicParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Topic{}, conversation.ErrTopicNotFound
	}
	if err != nil {
		return conversation.Topic{}, fmt.Errorf("getting default topic: %w", err)
	}
	return topicFromRow(row), nil
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
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicID: uuid(topicID),
		MemberID: uuid(memberID), Body: body, EventSeq: eventSeq,
	})
	if err != nil {
		return conversation.Message{}, fmt.Errorf("inserting message: %w", err)
	}
	return messageFromRow(row), nil
}

// CreateTopic inserts a named topic with an already validated name.
func (w Writer) CreateTopic(ctx context.Context, organizationID, channelID kernel.ID, name string) (conversation.Topic, error) {
	row, err := w.queries.CreateTopic(ctx, sqlcgen.CreateTopicParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), Name: pgtype.Text{String: name, Valid: true},
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
// organisation and channel, recording their latest move sequence.
func (w Writer) MoveMessages(ctx context.Context, organizationID, channelID, fromTopicID, toTopicID kernel.ID, messageIDs []kernel.ID, movedEventSeq int64) (int64, error) {
	moved, err := w.queries.MoveMessages(ctx, sqlcgen.MoveMessagesParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID),
		FromTopicID: uuid(fromTopicID), ToTopicID: uuid(toTopicID), MessageIds: uuids(messageIDs),
		MovedEventSeq: pgtype.Int8{Int64: movedEventSeq, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("moving messages: %w", err)
	}
	return moved, nil
}

// LastMessageBefore returns the previous channel message, or zero when absent.
func (w Writer) LastMessageBefore(ctx context.Context, organizationID, channelID kernel.ID, seq int64) (int64, error) {
	previous, err := w.queries.LastChannelMessageBefore(ctx, sqlcgen.LastChannelMessageBeforeParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), EventSeq: seq,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("finding previous channel message: %w", err)
	}
	return previous, nil
}

// TopicChangedBetween checks posts and moves in the composer's topic since its cursor.
func (w Writer) TopicChangedBetween(ctx context.Context, organizationID, channelID, topicID kernel.ID, after, before int64) (bool, error) {
	changed, err := w.queries.TopicChangedBetween(ctx, sqlcgen.TopicChangedBetweenParams{
		OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), TopicID: uuid(topicID), AfterSeq: after, BeforeSeq: before,
	})
	if err != nil {
		return false, fmt.Errorf("checking intervening topic messages: %w", err)
	}
	return changed, nil
}
