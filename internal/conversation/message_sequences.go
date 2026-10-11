package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// MessageSequences supplies channel bounds without exposing message storage.
type MessageSequences interface {
	// FirstMessageAfter returns the channel's first event_seq strictly above
	// cursor, or zero if none exists, in the transaction it was bound to.
	FirstMessageAfter(context.Context, kernel.ID, kernel.ID, int64) (int64, error)
	// HasMessages probes parallel half-open bounds in one transaction-bound statement.
	HasMessages(context.Context, kernel.ID, kernel.ID, []int64, []int64) (bool, error)
}
