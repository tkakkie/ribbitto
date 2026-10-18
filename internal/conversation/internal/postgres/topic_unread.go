package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// TopicUnreadIn binds topic counts to the caller's snapshot.
func TopicUnreadIn(s platform.Snapshot) conversation.TopicUnread {
	return topicUnread{sqlcgen.New(pgxbridge.Snapshot(s))}
}

type topicUnread struct{ queries *sqlcgen.Queries }

func (r topicUnread) Count(ctx context.Context, organizationID, channelID kernel.ID, prefix int64, topics []kernel.ID, floors []int64, readSet []conversation.SequenceRange, selected *kernel.ID) (map[kernel.ID]int64, int64, error) {
	return r.CountWithFirstUnread(ctx, organizationID, channelID, prefix, topics, floors, readSet, selected, true)
}

func (r topicUnread) CountWithFirstUnread(ctx context.Context, organizationID, channelID kernel.ID, prefix int64, topics []kernel.ID, floors []int64, readSet []conversation.SequenceRange, selected *kernel.ID, firstUnread bool) (map[kernel.ID]int64, int64, error) {
	if len(topics) != len(floors) {
		return nil, 0, fmt.Errorf("counting topic unread: unequal topic arrays")
	}
	ids := make([]pgtype.UUID, len(topics))
	for i, id := range topics {
		ids[i] = uuid(id)
	}
	// Containment excludes prefix-read moves without a lower sequence bound.
	readSet = append([]conversation.SequenceRange{{Lo: 0, Hi: prefix}}, readSet...)
	ranges := make(pgtype.Multirange[pgtype.Range[pgtype.Int8]], len(readSet))
	for i, r := range readSet {
		ranges[i] = pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: r.Lo, Valid: true}, Upper: pgtype.Int8{Int64: r.Hi, Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}
	}
	var selection pgtype.UUID
	if selected != nil {
		selection = uuid(*selected)
	}
	rows, err := r.queries.CountTopicUnread(ctx, sqlcgen.CountTopicUnreadParams{WithFirstUnread: firstUnread, OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), PrefixBefore: prefix - 1, TopicIds: ids, Floors: floors, ReadSet: ranges, Selected: selection})
	if err != nil {
		return nil, 0, fmt.Errorf("counting topic unread: %w", err)
	}
	counts := make(map[kernel.ID]int64, len(rows))
	var first int64
	for _, row := range rows {
		counts[row.TopicID.Bytes] = row.UnreadCount
		if selected != nil && row.TopicID.Bytes == *selected {
			first = row.FirstUnread
		}
	}
	return counts, first, nil
}
