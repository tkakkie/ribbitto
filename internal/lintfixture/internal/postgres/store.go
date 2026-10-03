//go:build lintfixture

package postgres

import (
	"github.com/jackc/pgx/v5"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// Unwrap is what a store does to run its queries in the caller's transaction.
func Unwrap(tx platform.Tx) pgx.Tx { return pgxbridge.Tx(tx) }
