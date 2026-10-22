package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// Writer unions ranges in a caller-owned transaction.
type Writer struct {
	tx      pgx.Tx
	queries *sqlcgen.Queries
}

// WriterIn binds a writer to tx; the caller must roll back on any error.
func WriterIn(tx platform.Tx) *Writer {
	p := pgxbridge.Tx(tx)
	return &Writer{tx: p, queries: sqlcgen.New(p)}
}

// Merge locks scope, inserts its join prefix before any other range, and
// unions added with only its overlapping or touching neighbours.
// joinedEventSeq is the member's persisted join sequence supplied by org.
func (w *Writer) Merge(ctx context.Context, scope unread.Scope, joinedEventSeq int64, added conversation.SequenceRange) error {
	_, err := w.MergeCovered(ctx, scope, joinedEventSeq, added)
	return err
}

// MergeCovered performs the same locked union and returns added minus stored
// coverage after establishing the implicit join prefix. The lock lasts until tx ends.
func (w *Writer) MergeCovered(ctx context.Context, scope unread.Scope, joinedEventSeq int64, added conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	if joinedEventSeq < 0 || joinedEventSeq == math.MaxInt64 || added.Lo < 0 || added.Hi <= added.Lo {
		return nil, fmt.Errorf("merging read range: invalid bounds or join sequence")
	}
	params := scopeParams(scope)
	if err := w.lock(ctx, params); err != nil {
		return nil, err
	}
	if err := w.merge(ctx, params, conversation.SequenceRange{Lo: 0, Hi: joinedEventSeq}); err != nil {
		return nil, err
	}
	return w.mergeCovered(ctx, params, added)
}

func scopeParams(scope unread.Scope) sqlcgen.LockChannelReadParams {
	return sqlcgen.LockChannelReadParams{OrganizationID: pgtype.UUID{Bytes: scope.OrganizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: scope.ChannelID, Valid: true}, MemberID: pgtype.UUID{Bytes: scope.MemberID, Valid: true}}
}

func (w *Writer) lock(ctx context.Context, params sqlcgen.LockChannelReadParams) error {
	if _, err := w.queries.LockChannelRead(ctx, params); errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT is outside the query gate. A savepoint protects the outer
		// transaction when another writer creates this same key first.
		save, err := w.tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("starting lock-row savepoint: %w", err)
		}
		err = sqlcgen.New(save).InsertChannelRead(ctx, sqlcgen.InsertChannelReadParams(params))
		if err != nil {
			if rollbackErr := save.Rollback(ctx); rollbackErr != nil {
				return fmt.Errorf("rolling back lock-row savepoint: %w", rollbackErr)
			}
			var conflict *pgconn.PgError
			if !errors.As(err, &conflict) || conflict.Code != "23505" || conflict.ConstraintName != "channel_read_pkey" {
				return fmt.Errorf("creating channel read lock: %w", err)
			}
		} else if err = save.Commit(ctx); err != nil {
			return fmt.Errorf("releasing lock-row savepoint: %w", err)
		}
		if _, err = w.queries.LockChannelRead(ctx, params); err != nil {
			return fmt.Errorf("locking created channel read: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("locking channel read: %w", err)
	}
	return nil
}

func (w *Writer) merge(ctx context.Context, scope sqlcgen.LockChannelReadParams, added conversation.SequenceRange) error {
	_, err := w.mergeCovered(ctx, scope, added)
	return err
}

func (w *Writer) mergeCovered(ctx context.Context, scope sqlcgen.LockChannelReadParams, added conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	lower := added.Lo
	previous, err := w.queries.RangePredecessor(ctx, sqlcgen.RangePredecessorParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, Lo: added.Lo})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("finding read range predecessor: %w", err)
	}
	if err == nil {
		lower = previous.Lo
	}
	removed, err := w.queries.DeleteRangeNeighbours(ctx, sqlcgen.DeleteRangeNeighboursParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, LowerLo: lower, Lo: added.Lo, Hi: added.Hi})
	if err != nil {
		return nil, fmt.Errorf("removing read range neighbours: %w", err)
	}
	// DELETE RETURNING has no order. Subtract before widening added to the union.
	slices.SortFunc(removed, func(a, b sqlcgen.DeleteRangeNeighboursRow) int { return cmp.Compare(a.Lo, b.Lo) })
	var covered []conversation.SequenceRange
	lo := added.Lo
	for _, r := range removed {
		if lo < r.Lo {
			covered = append(covered, conversation.SequenceRange{Lo: lo, Hi: min(r.Lo, added.Hi)})
		}
		lo = max(lo, r.Hi)
	}
	if lo < added.Hi {
		covered = append(covered, conversation.SequenceRange{Lo: lo, Hi: added.Hi})
	}
	// Existing ranges never touch, so expanding to the last neighbour's Hi
	// cannot reach another range beyond the original upper bound.
	for _, r := range removed {
		added.Lo = min(added.Lo, r.Lo)
		added.Hi = max(added.Hi, r.Hi)
	}
	if err = w.queries.InsertReadRange(ctx, sqlcgen.InsertReadRangeParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, Lo: added.Lo, Hi: added.Hi}); err != nil {
		return nil, fmt.Errorf("inserting merged read range: %w", err)
	}
	return covered, nil
}
