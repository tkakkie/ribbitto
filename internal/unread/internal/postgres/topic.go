package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// TopicWrite holds prepared read state and the channel lock in its caller's
// transaction. The caller must roll back that transaction on any error.
type TopicWrite struct {
	Prefix, Floor int64
	Ranges        []unread.Range
	w             *Writer
	scope         sqlcgen.LockChannelReadParams
	topic         pgtype.UUID
	hasFloor      bool
}

// Prepare locks the channel, establishes its join prefix and loads the read
// set above it and the topic floor (Prefix - 1 when absent).
func (w *Writer) Prepare(ctx context.Context, scope unread.TopicScope, joinedEventSeq int64) (*TopicWrite, error) {
	if joinedEventSeq < 0 || joinedEventSeq == math.MaxInt64 {
		return nil, fmt.Errorf("preparing topic read: invalid join sequence")
	}
	s := scopeParams(scope.Scope)
	if err := w.lock(ctx, s); err != nil {
		return nil, err
	}
	if err := w.merge(ctx, s, unread.Range{Lo: 0, Hi: joinedEventSeq + 1}); err != nil {
		return nil, err
	}
	rs, err := w.queries.TopicReadRanges(ctx, sqlcgen.TopicReadRangesParams(s))
	if err != nil {
		return nil, fmt.Errorf("loading topic read ranges: %w", err)
	}
	p := &TopicWrite{w: w, scope: s, topic: pgtype.UUID{Bytes: scope.TopicID, Valid: true}, Prefix: rs[0].Hi}
	for _, r := range rs[1:] {
		p.Ranges = append(p.Ranges, unread.Range{Lo: r.Lo, Hi: r.Hi})
	}
	p.Floor, err = w.queries.TopicReadFloor(ctx, sqlcgen.TopicReadFloorParams{OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, TopicID: p.topic})
	p.hasFloor = err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		p.Floor = p.Prefix - 1
	} else if err != nil {
		return nil, fmt.Errorf("loading topic read floor: %w", err)
	}
	return p, nil
}

// Add unions parallel range bounds and raises the floor, even for an empty
// batch. Cursor validation against committed events belongs to the caller.
func (p *TopicWrite) Add(ctx context.Context, los, his []int64, cursor int64) error {
	if len(los) != len(his) {
		return fmt.Errorf("adding topic ranges: unequal bound lengths")
	}
	if cursor < 0 || cursor == math.MaxInt64 {
		return unread.ErrInvalidCursor
	}
	rs := make([]unread.Range, len(los))
	for i := range los {
		if los[i] < 0 || his[i] <= los[i] {
			return fmt.Errorf("adding topic ranges: invalid bounds")
		}
		rs[i] = unread.Range{Lo: los[i], Hi: his[i]}
	}
	rs = coalesce(rs)
	los, his = bounds(rs)
	s := p.scope
	touched, err := p.w.queries.BatchRangeNeighbours(ctx, sqlcgen.BatchRangeNeighboursParams{OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, Los: los, His: his})
	if err != nil {
		return fmt.Errorf("finding batch range neighbours: %w", err)
	}
	removed := make([]int64, len(touched))
	for i, r := range touched {
		removed[i] = r.Lo
		rs = append(rs, unread.Range{Lo: r.Lo, Hi: r.Hi})
	}
	if err = p.w.queries.DeleteBatchRanges(ctx, sqlcgen.DeleteBatchRangesParams{OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, Los: removed}); err != nil {
		return fmt.Errorf("deleting batch read ranges: %w", err)
	}
	// A stored range can connect several additions; coalesce again before insert.
	los, his = bounds(coalesce(rs))
	if err = p.w.queries.InsertBatchRanges(ctx, sqlcgen.InsertBatchRangesParams{OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, Los: los, His: his}); err != nil {
		return fmt.Errorf("inserting batch read ranges: %w", err)
	}
	// An absent floor reads as Prefix - 1, so an older cursor must not store a
	// lower one: the effective floor only rises.
	p.Floor = max(p.Floor, cursor)
	floor := sqlcgen.InsertTopicReadFloorParams{OrganizationID: s.OrganizationID, ChannelID: s.ChannelID, MemberID: s.MemberID, TopicID: p.topic, FloorSeq: p.Floor}
	if p.hasFloor {
		err = p.w.queries.RaiseTopicReadFloor(ctx, sqlcgen.RaiseTopicReadFloorParams(floor))
	} else {
		err = p.w.queries.InsertTopicReadFloor(ctx, floor)
	}
	if err != nil {
		return fmt.Errorf("raising topic read floor: %w", err)
	}
	p.hasFloor = true
	return nil
}

func coalesce(rs []unread.Range) []unread.Range {
	slices.SortFunc(rs, func(a, b unread.Range) int {
		return cmp.Compare(a.Lo, b.Lo)
	})
	result := rs[:0]
	for _, r := range rs {
		if len(result) > 0 && r.Lo <= result[len(result)-1].Hi {
			result[len(result)-1].Hi = max(result[len(result)-1].Hi, r.Hi)
		} else {
			result = append(result, r)
		}
	}
	return result
}

func bounds(rs []unread.Range) (los, his []int64) {
	los, his = make([]int64, len(rs)), make([]int64, len(rs))
	for i, r := range rs {
		los[i], his[i] = r.Lo, r.Hi
	}
	return
}
