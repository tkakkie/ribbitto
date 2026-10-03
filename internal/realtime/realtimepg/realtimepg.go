package realtimepg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres"
)

// NewReader returns realtime's event reader on pool. bounds reads org's
// cursor bounds in the reader's snapshot (infra/postgres's EventBoundsIn
// until step 3), and kinds registers each kind's publisher Router.
func NewReader(pool *pgxpool.Pool, bounds realtime.BoundsIn, kinds realtime.Kinds) realtime.EventReader {
	return postgres.NewReader(pool, bounds, kinds)
}

// AppenderIn returns realtime's event appender bound to a writer's
// transaction. Consumers declare the interface they need and adapt to it
// with a closure (decision 26).
func AppenderIn(tx platform.Tx) *postgres.Appender { return postgres.AppenderIn(tx) }

// NewCleaner returns realtime's retention cleaner on pool. boundary locks an
// organisation and raises its replay boundary in the cleaner's transaction
// (infra/postgres's RetentionBoundaryIn until step 3).
func NewCleaner(pool *pgxpool.Pool, boundary realtime.RetentionBoundaryIn) realtime.EventCleaner {
	return postgres.NewCleaner(pool, boundary)
}
