package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ExpireBatch runs one batch in tx, for tests that hold the transaction open.
func (c *Cleaner) ExpireBatch(ctx context.Context, tx platform.Tx, organizationID kernel.ID, cutoff time.Time) (int64, error) {
	return c.expireBatch(ctx, tx, organizationID, pgtype.Timestamptz{Time: cutoff, Valid: true})
}
