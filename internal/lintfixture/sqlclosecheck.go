//go:build lintfixture

package lintfixture

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UnclosedRows must fail sqlclosecheck: the pgx rows are unused and unclosed.
// Any other rows use satisfies it; keep defer rows.Close() in real queries,
// since lint does not catch a missing Close once rows are used.
func UnclosedRows(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("querying rows: %w", err)
	}
	_ = rows
	return nil
}
