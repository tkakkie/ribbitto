//go:build lintfixture

package lintfixture

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UnclosedRows must fail sqlclosecheck: the query result is never closed.
func UnclosedRows(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("querying rows: %w", err)
	}
	_ = rows
	return nil
}
