package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// EventCleaner owns retention transactions, committing each organisation's batches separately.
type EventCleaner struct{ pool *pgxpool.Pool }

// NewEventCleaner returns a cleaner using pool.
func NewEventCleaner(pool *pgxpool.Pool) *EventCleaner { return &EventCleaner{pool: pool} }

// ExpireEvents deletes expired events in sequence order, at most 1,000 per
// transaction. An error or cancellation stops the run but keeps committed batches.
func (c *EventCleaner) ExpireEvents(ctx context.Context, cutoff time.Time) error {
	expiry := pgtype.Timestamptz{Time: cutoff, Valid: true}
	organizations, err := sqlcgen.New(c.pool).OrganizationsWithExpiredEvents(ctx, expiry)
	if err != nil {
		return fmt.Errorf("listing organisations with expired events: %w", err)
	}
	for _, id := range organizations {
		for {
			count, err := c.expireBatch(ctx, id, expiry)
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

func (c *EventCleaner) expireBatch(ctx context.Context, id pgtype.UUID, cutoff pgtype.Timestamptz) (int64, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning event retention: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	// Posting takes this lock first too; no other organisation is locked.
	if err := q.LockEventRetentionOrganization(ctx, id); err != nil {
		return 0, fmt.Errorf("locking event retention organisation: %w", err)
	}
	count, err := q.ExpireEventBatch(ctx, sqlcgen.ExpireEventBatchParams{OrganizationID: id, Cutoff: cutoff})
	if err != nil {
		return 0, fmt.Errorf("deleting event batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing event retention: %w", err)
	}
	return count, nil
}
