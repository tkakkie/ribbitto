package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventAppender is what the event-writing flows (posting, branching, setup
// and sign-up) need from realtime's event log inside their transaction.
// infra declares it as their consumer until the flows move (org in step 3,
// conversation in step 4); wiring injects the implementation (decision 26).
type EventAppender interface {
	AppendMessagePosted(ctx context.Context, organizationID, channelID, messageID, topicID domain.ID, seq int64) error
	AppendMessagesMoved(ctx context.Context, organizationID, channelID, fromTopicID, toTopicID domain.ID, messageIDs []domain.ID, seq int64) error
	AppendMemberJoined(ctx context.Context, organizationID, memberID domain.ID, seq int64) error
}

// EventAppenders binds an EventAppender to a flow's transaction.
type EventAppenders func(platform.Tx) EventAppender

// EventLogIn binds EventLog, today's EventAppender, to tx.
func EventLogIn(tx platform.Tx) EventAppender { return NewEventLog(pgxbridge.Tx(tx)) }

// EventLog owns realtime's event inserts within a caller-owned transaction.
type EventLog struct{ queries *sqlcgen.Queries }

// NewEventLog binds the writer to tx so events commit or roll back with
// their entities. The caller allocates the sequence and inserts the entity
// before appending its event, and remains responsible for committing tx.
func NewEventLog(tx pgx.Tx) *EventLog {
	return &EventLog{queries: sqlcgen.New(tx)}
}

// AppendMessagePosted records an organisation-wide message event at the
// sequence already allocated for the message, with its posting-time topic.
func (l *EventLog) AppendMessagePosted(ctx context.Context, organizationID, channelID, messageID, topicID domain.ID, seq int64) error {
	data, err := message.EncodePosted(channelID, messageID, topicID)
	if err == nil {
		err = l.insert(ctx, organizationID, seq, realtime.EventMessagePosted, data)
	}
	if err != nil {
		return fmt.Errorf("appending message event: %w", err)
	}
	return nil
}

// AppendMessagesMoved records an organisation-wide move of messageIDs from
// one topic of the channel to another, at the sequence allocated for it.
func (l *EventLog) AppendMessagesMoved(ctx context.Context, organizationID, channelID, fromTopicID, toTopicID domain.ID, messageIDs []domain.ID, seq int64) error {
	data, err := topic.EncodeMoved(topic.Moved{ChannelID: channelID, FromTopicID: fromTopicID, ToTopicID: toTopicID, MessageIDs: messageIDs})
	if err == nil {
		err = l.insert(ctx, organizationID, seq, realtime.EventMessagesMoved, data)
	}
	if err != nil {
		return fmt.Errorf("appending move event: %w", err)
	}
	return nil
}

// AppendMemberJoined records an organisation-wide join event at the
// sequence already allocated for the membership.
func (l *EventLog) AppendMemberJoined(ctx context.Context, organizationID, memberID domain.ID, seq int64) error {
	data, err := member.EncodeJoined(memberID)
	if err == nil {
		err = l.insert(ctx, organizationID, seq, realtime.EventMemberJoined, data)
	}
	if err != nil {
		return fmt.Errorf("appending member event: %w", err)
	}
	return nil
}

func (l *EventLog) insert(ctx context.Context, organizationID domain.ID, seq int64, kind realtime.EventKind, data []byte) error {
	return l.queries.InsertEvent(ctx, sqlcgen.InsertEventParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Seq: seq, Kind: string(kind), Data: data,
	})
}
