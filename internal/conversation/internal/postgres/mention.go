package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// InsertMentions writes one batch using the message insert's returned sequence.
func (w Writer) InsertMentions(ctx context.Context, organizationID, messageID kernel.ID, eventSeq int64, memberIDs []kernel.ID) error {
	if len(memberIDs) == 0 {
		return nil
	}
	err := w.queries.InsertMessageMentions(ctx, sqlcgen.InsertMessageMentionsParams{
		OrganizationID: uuid(organizationID), MessageID: uuid(messageID),
		EventSeq: eventSeq, MemberIds: uuids(memberIDs),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "message_mention_member_fkey" {
		return fmt.Errorf("%w: %w", conversation.ErrInvalidBody, conversation.ErrUnknownReference)
	}
	if err != nil {
		return fmt.Errorf("inserting message mentions: %w", err)
	}
	return nil
}
