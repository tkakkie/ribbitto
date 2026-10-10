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

// ChannelUnreadIn binds capped message counts to the caller's snapshot.
func ChannelUnreadIn(s platform.Snapshot) conversation.ChannelUnread {
	return channelUnread{sqlcgen.New(pgxbridge.Snapshot(s))}
}

type channelUnread struct{ queries *sqlcgen.Queries }

func (r channelUnread) Count(ctx context.Context, organizationID kernel.ID, channels, gapChannels []kernel.ID, los, his []int64) (map[kernel.ID]int64, map[kernel.ID]int64, error) {
	if len(gapChannels) != len(los) || len(los) != len(his) {
		return nil, nil, fmt.Errorf("counting channel unread: unequal gap arrays")
	}
	ids, gaps := make([]pgtype.UUID, len(channels)), make([]pgtype.UUID, len(gapChannels))
	before := make([]int64, len(los))
	for i, id := range channels {
		ids[i] = uuid(id)
	}
	for i, id := range gapChannels {
		if los[i] < 0 || his[i] <= los[i] {
			return nil, nil, fmt.Errorf("counting channel unread: invalid gap")
		}
		gaps[i], before[i] = uuid(id), los[i]-1
	}
	rows, err := r.queries.CountChannelUnread(ctx, sqlcgen.CountChannelUnreadParams{
		OrganizationID: uuid(organizationID), ChannelIds: ids, GapChannelIds: gaps, GapLosBefore: before, GapHis: his,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("counting channel unread: %w", err)
	}
	counts, first := make(map[kernel.ID]int64, len(rows)), make(map[kernel.ID]int64, len(rows))
	for _, row := range rows {
		counts[row.ChannelID.Bytes], first[row.ChannelID.Bytes] = row.UnreadCount, row.FirstUnread
	}
	return counts, first, nil
}
