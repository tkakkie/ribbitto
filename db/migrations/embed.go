// Package migrations embeds the schema owned by internal/infra/postgres.
package migrations

import "embed"

// FS contains the SQL migrations shipped with the binary.
//
//go:embed *.sql
var FS embed.FS
