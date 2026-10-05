// Package migrations embeds the schema of every module's tables, which
// internal/platform/postgres runs.
package migrations

import "embed"

// FS contains the SQL migrations shipped with the binary.
//
//go:embed *.sql
var FS embed.FS
