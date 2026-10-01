package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// EventLog owns realtime's event inserts within a caller-owned transaction.
type EventLog struct{ queries *sqlcgen.Queries }

// NewEventLog binds the writer to tx so events commit or roll back with
// their entities. The caller allocates the sequence and inserts the entity
// before appending its event, and remains responsible for committing tx.
func NewEventLog(tx pgx.Tx) *EventLog {
	return &EventLog{queries: sqlcgen.New(tx)}
}

// AppendMessagePosted records an organisation-wide message event at the
// sequence already allocated for the message.
func (l *EventLog) AppendMessagePosted(ctx context.Context, organizationID, channelID, messageID domain.ID, seq int64) error {
	err := l.queries.InsertMessageEvent(ctx, sqlcgen.InsertMessageEventParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Seq: seq, Kind: string(domain.EventMessagePosted),
		ChannelID: pgtype.UUID{Bytes: channelID, Valid: true}, MessageID: pgtype.UUID{Bytes: messageID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("appending message event: %w", err)
	}
	return nil
}

// AppendMemberJoined records an organisation-wide join event at the
// sequence already allocated for the membership.
func (l *EventLog) AppendMemberJoined(ctx context.Context, organizationID, memberID domain.ID, seq int64) error {
	err := l.queries.InsertMemberEvent(ctx, sqlcgen.InsertMemberEventParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Seq: seq, Kind: string(domain.EventMemberJoined),
		MemberID: pgtype.UUID{Bytes: memberID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("appending member event: %w", err)
	}
	return nil
}
