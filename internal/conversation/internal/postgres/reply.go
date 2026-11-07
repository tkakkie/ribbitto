package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ListReplies reads one bounded source for the member, with no directory reads.
func (s ReadStore) ListReplies(ctx context.Context, organizationID, memberID kernel.ID, channelIDs []kernel.ID, before *int64) ([]conversation.Message, error) {
	params := sqlcgen.ListRepliesParams{
		OrganizationID: uuid(organizationID), MemberID: uuid(memberID), ChannelIds: uuids(channelIDs),
		RowLimit: conversation.PageSize + 1,
	}
	if before != nil {
		params.BeforeEventSeq = pgtype.Int8{Int64: *before, Valid: true}
	}
	rows, err := s.queries.ListReplies(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing replies: %w", err)
	}
	return messagesFromRows(rows), nil
}
