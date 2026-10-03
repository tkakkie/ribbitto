package pgtest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// New returns a pool on a fresh clone of the migrated template. The database
// lifecycle lives in internal/platform/postgres/pgtest; this package keeps its
// API, and the feature fixtures below, until each module moves its own
// (decision 26, removed in the migration's last step).
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return platform.New(t)
}

// NewEmpty returns a pool on a fresh database without application migrations.
func NewEmpty(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return platform.NewEmpty(t)
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
