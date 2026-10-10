package unreadpg

import (
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres"
)

// WriterIn binds the read-range writer to the caller's transaction.
func WriterIn(tx platform.Tx) *postgres.Writer { return postgres.WriterIn(tx) }
