package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres/sqlcgen"
)

// Cleaner owns retention transactions, committing each organisation's
// batches separately. org's lock and boundary come through boundary.
type Cleaner struct {
	pool     *pgxpool.Pool
	boundary realtime.RetentionBoundaryIn
}

// NewCleaner returns a cleaner using pool and boundary.
func NewCleaner(pool *pgxpool.Pool, boundary realtime.RetentionBoundaryIn) *Cleaner {
	return &Cleaner{pool: pool, boundary: boundary}
}

// ExpireEvents deletes expired events in sequence order, at most 1,000 per
// transaction. An error or cancellation stops the run but keeps committed batches.
func (c *Cleaner) ExpireEvents(ctx context.Context, cutoff time.Time) error {
	expiry := pgtype.Timestamptz{Time: cutoff, Valid: true}
	organizations, err := sqlcgen.New(c.pool).OrganizationsWithExpiredEvents(ctx, expiry)
	if err != nil {
		return fmt.Errorf("listing organisations with expired events: %w", err)
	}
	for _, id := range organizations {
		for {
			var count int64
			err := platform.InTx(ctx, c.pool, func(tx platform.Tx) error {
				var err error
				count, err = c.expireBatch(ctx, tx, id.Bytes, expiry)
				return err
			})
			if err != nil {
				return fmt.Errorf("expiring events for organisation %s: %w", id, err)
			}
			if count == 0 {
				break
			}
		}
	}
	return nil
}

// expireBatch runs one batch in tx: lock, delete, raise the boundary.
func (c *Cleaner) expireBatch(ctx context.Context, tx platform.Tx, organizationID kernel.ID, cutoff pgtype.Timestamptz) (int64, error) {
	boundary := c.boundary(tx)
	// Posting takes this lock first too; no other organisation is locked.
	if err := boundary.LockForRetention(ctx, organizationID); err != nil {
		return 0, fmt.Errorf("locking event retention organisation: %w", err)
	}
	q := sqlcgen.New(pgxbridge.Tx(tx))
	lowest, err := q.LowestEvents(ctx, uuid(organizationID))
	if err != nil {
		return 0, fmt.Errorf("reading the oldest events: %w", err)
	}
	// Only the expired prefix in seq order goes: created_at is the writing
	// transaction's start, which need not follow seq, and the boundary must
	// never pass a row still in the log (#430).
	var through int64
	for _, event := range lowest {
		if !event.CreatedAt.Time.Before(cutoff.Time) {
			break
		}
		through = event.Seq
	}
	if through == 0 {
		return 0, nil
	}
	row, err := q.DeleteEventsThrough(ctx, sqlcgen.DeleteEventsThroughParams{
		OrganizationID: uuid(organizationID), Through: through,
	})
	if err != nil {
		return 0, fmt.Errorf("deleting event batch: %w", err)
	}
	if err := boundary.RaiseBoundary(ctx, organizationID, row.Through); err != nil {
		return 0, fmt.Errorf("raising the replay boundary: %w", err)
	}
	return row.Deleted, nil
}
