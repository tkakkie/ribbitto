package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// PersistedIn binds unbounded raw read-state capture to the caller's snapshot.
func PersistedIn(s platform.Snapshot) unread.PersistedReader {
	return persistedReader{sqlcgen.New(pgxbridge.Snapshot(s))}
}

type persistedReader struct{ queries *sqlcgen.Queries }

func (r persistedReader) Read(ctx context.Context, scope unread.Scope, channels, topics []kernel.ID) (unread.PersistedState, error) {
	ids := func(values []kernel.ID) []pgtype.UUID {
		result := make([]pgtype.UUID, len(values))
		for i, id := range values {
			result[i] = pgtype.UUID{Bytes: id, Valid: true}
		}
		return result
	}
	s := scopeParams(scope)
	ranges, err := r.queries.WitnessRanges(ctx, sqlcgen.WitnessRangesParams{OrganizationID: s.OrganizationID, MemberID: s.MemberID, ChannelIds: ids(channels)})
	if err != nil {
		return unread.PersistedState{}, fmt.Errorf("reading persisted ranges: %w", err)
	}
	floors, err := r.queries.WitnessFloors(ctx, sqlcgen.WitnessFloorsParams{OrganizationID: s.OrganizationID, MemberID: s.MemberID, ChannelID: s.ChannelID, TopicIds: ids(topics)})
	if err != nil {
		return unread.PersistedState{}, fmt.Errorf("reading persisted floors: %w", err)
	}
	state := unread.PersistedState{Floors: make(map[kernel.ID]int64, len(floors))}
	for _, row := range ranges {
		state.Ranges = append(state.Ranges, unread.PersistedRange{ChannelID: row.ChannelID.Bytes, Lo: row.Lo, Hi: row.Hi})
	}
	for _, row := range floors {
		state.Floors[row.TopicID.Bytes] = row.FloorSeq
	}
	return state, nil
}
