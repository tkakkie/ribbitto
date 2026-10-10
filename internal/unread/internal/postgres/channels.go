package postgres

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// ChannelRangesIn binds the range loader to a caller-owned snapshot.
func ChannelRangesIn(s platform.Snapshot) unread.ChannelRanges {
	return channelRanges{queries: sqlcgen.New(pgxbridge.Snapshot(s))}
}

type channelRanges struct{ queries *sqlcgen.Queries }

func (r channelRanges) Read(ctx context.Context, organizationID, memberID kernel.ID, joined int64, channels []kernel.ID) (map[kernel.ID]unread.ChannelReadState, error) {
	if joined < 0 || joined == math.MaxInt64 {
		return nil, fmt.Errorf("reading channel ranges: invalid join sequence")
	}
	ids := make([]pgtype.UUID, len(channels))
	states := make(map[kernel.ID]unread.ChannelReadState, len(channels))
	for i, id := range channels {
		ids[i] = pgtype.UUID{Bytes: id, Valid: true}
		states[id] = unread.ChannelReadState{Prefix: joined + 1}
	}
	rows, err := r.queries.FirstChannelReadRanges(ctx, sqlcgen.FirstChannelReadRangesParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		MemberID:       pgtype.UUID{Bytes: memberID, Valid: true}, ChannelIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("reading channel ranges: %w", err)
	}
	stored := make(map[kernel.ID][]unread.Range)
	for _, row := range rows {
		stored[row.ChannelID.Bytes] = append(stored[row.ChannelID.Bytes], unread.Range{Lo: row.Lo, Hi: row.Hi})
	}
	for id, state := range states {
		ranges := stored[id]
		if len(ranges) == 0 {
			state.Gaps = []unread.Range{{Lo: state.Prefix, Hi: math.MaxInt64}}
		} else {
			state.Prefix = ranges[0].Hi
			for i, span := range ranges {
				// The lookahead bounds gap 100; it is never an open gap.
				if i == 100 {
					break
				}
				hi := int64(math.MaxInt64)
				if i+1 < len(ranges) {
					hi = ranges[i+1].Lo
				}
				state.Gaps = append(state.Gaps, unread.Range{Lo: span.Hi, Hi: hi})
			}
		}
		states[id] = state
	}
	return states, nil
}
