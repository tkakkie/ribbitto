package realtimepg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres"
)

// NewReader returns realtime's event reader on pool. bounds reads org's
// cursor bounds in the reader's snapshot (infra/postgres's EventBoundsIn
// until step 3), and kinds registers each kind's publisher Router.
func NewReader(pool *pgxpool.Pool, bounds realtime.BoundsIn, kinds realtime.Kinds) realtime.EventReader {
	return postgres.NewReader(pool, bounds, kinds)
}
