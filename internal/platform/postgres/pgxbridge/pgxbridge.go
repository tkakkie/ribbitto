package pgxbridge

import (
	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/internal/handle"
)

// Tx returns the pgx transaction inside t, for a store's queries.
func Tx(t postgres.Tx) pgx.Tx { return handle.UnwrapTx(t) }

// Snapshot returns the read-only pgx transaction inside s.
func Snapshot(s postgres.Snapshot) pgx.Tx { return handle.UnwrapSnapshot(s) }
