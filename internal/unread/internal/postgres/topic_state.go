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

// TopicStateIn binds read-state loading to a caller-owned snapshot.
func TopicStateIn(s platform.Snapshot) unread.TopicStateReader {
	return topicStateReader{queries: sqlcgen.New(pgxbridge.Snapshot(s))}
}

type topicStateReader struct{ queries *sqlcgen.Queries }

func (r topicStateReader) Read(ctx context.Context, scope unread.Scope, joined int64, listed []kernel.ID, selected *kernel.ID) (unread.TopicReadState, error) {
	if joined < 0 || joined == math.MaxInt64 {
		return unread.TopicReadState{}, fmt.Errorf("reading topic state: invalid join sequence")
	}
	ids := make([]pgtype.UUID, 0, len(listed)+1)
	seen := make(map[kernel.ID]bool)
	requested := append([]kernel.ID(nil), listed...)
	if selected != nil {
		requested = append(requested, *selected)
	}
	for _, id := range requested {
		if !seen[id] {
			ids, seen[id] = append(ids, pgtype.UUID{Bytes: id, Valid: true}), true
		}
	}
	if len(ids) > 51 {
		return unread.TopicReadState{}, fmt.Errorf("reading topic state: more than 51 topics")
	}
	s := scopeParams(scope)
	rows, err := r.queries.ReadTopicState(ctx, sqlcgen.ReadTopicStateParams{
		OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, TopicIds: ids,
	})
	if err != nil {
		return unread.TopicReadState{}, fmt.Errorf("reading topic state: %w", err)
	}
	state := unread.TopicReadState{Prefix: joined + 1, Floors: make(map[kernel.ID]int64, len(ids))}
	for _, row := range rows {
		if row.TopicN == 0 {
			// The stored set starts at zero; disjoint ranges never touch it.
			if row.Value == 0 {
				state.Prefix = row.Hi
			} else {
				state.Ranges = append(state.Ranges, unread.Range{Lo: row.Value, Hi: row.Hi})
			}
		} else {
			state.Floors[ids[row.TopicN-1].Bytes] = row.Value
		}
	}
	for _, id := range ids {
		if _, ok := state.Floors[id.Bytes]; !ok {
			state.Floors[id.Bytes] = state.Prefix - 1
		}
	}
	return state, nil
}
