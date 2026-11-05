package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
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

// ReadableChannelIDs implements today's public-channel rule without org reads.
func (s ReadStore) ReadableChannelIDs(ctx context.Context, m org.Membership) ([]kernel.ID, error) {
	channels, err := s.ListChannels(ctx, m.Organization.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]kernel.ID, len(channels))
	for i, channel := range channels {
		ids[i] = channel.ID
	}
	return ids, nil
}

// ListMentions reads one bounded source for the member, with no directory reads.
func (s ReadStore) ListMentions(ctx context.Context, organizationID, memberID kernel.ID, channelIDs []kernel.ID, before *int64) ([]conversation.Message, error) {
	params := sqlcgen.ListMentionsParams{
		OrganizationID: uuid(organizationID), MemberID: uuid(memberID), ChannelIds: uuids(channelIDs),
		RowLimit: conversation.PageSize + 1,
	}
	if before != nil {
		params.BeforeEventSeq = pgtype.Int8{Int64: *before, Valid: true}
	}
	rows, err := s.queries.ListMentions(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing mentions: %w", err)
	}
	return messagesFromRows(rows), nil
}
