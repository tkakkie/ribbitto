package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
func (w *Writer) Merge(ctx context.Context, scope unread.Scope, joinedEventSeq int64, added unread.Range) error {
	if joinedEventSeq < 0 || joinedEventSeq == math.MaxInt64 || added.Lo < 0 || added.Hi <= added.Lo {
		return fmt.Errorf("merging read range: invalid bounds or join sequence")
	}
	params := sqlcgen.LockChannelReadParams{OrganizationID: pgtype.UUID{Bytes: scope.OrganizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: scope.ChannelID, Valid: true}, MemberID: pgtype.UUID{Bytes: scope.MemberID, Valid: true}}
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
	if err := w.merge(ctx, params, unread.Range{Lo: 0, Hi: joinedEventSeq + 1}); err != nil {
		return err
	}
	return w.merge(ctx, params, added)
}

func (w *Writer) merge(ctx context.Context, scope sqlcgen.LockChannelReadParams, added unread.Range) error {
	lower := added.Lo
	previous, err := w.queries.RangePredecessor(ctx, sqlcgen.RangePredecessorParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, Lo: added.Lo})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("finding read range predecessor: %w", err)
	}
	if err == nil {
		lower = previous.Lo
	}
	removed, err := w.queries.DeleteRangeNeighbours(ctx, sqlcgen.DeleteRangeNeighboursParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, LowerLo: lower, Lo: added.Lo, Hi: added.Hi})
	if err != nil {
		return fmt.Errorf("removing read range neighbours: %w", err)
	}
	// Existing ranges never touch, so expanding to the last neighbour's Hi
	// cannot reach another range beyond the original upper bound.
	for _, r := range removed {
		added.Lo = min(added.Lo, r.Lo)
		added.Hi = max(added.Hi, r.Hi)
	}
	if err = w.queries.InsertReadRange(ctx, sqlcgen.InsertReadRangeParams{OrganizationID: scope.OrganizationID, ChannelID: scope.ChannelID, MemberID: scope.MemberID, Lo: added.Lo, Hi: added.Hi}); err != nil {
		return fmt.Errorf("inserting merged read range: %w", err)
	}
	return nil
}
