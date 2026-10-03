//go:build lintfixture

package badroot

import (
	"github.com/jackc/pgx/v5"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// Unwrap is what a module root must not be able to do.
func Unwrap(tx platform.Tx) pgx.Tx { return pgxbridge.Tx(tx) }
