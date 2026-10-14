package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/platform/postgres/internal/handle"
)

// CaptureSnapshot isolates optional reads with a savepoint in the same transaction.
// It discards capture errors without logging them; false means no usable capture.
// A broken connection still fails the outer snapshot's completion.
func CaptureSnapshot(ctx context.Context, s Snapshot, read func(Snapshot) error) bool {
	save, err := handle.UnwrapSnapshot(s).Begin(ctx)
	if err != nil {
		return false
	}
	if err := read(handle.NewSnapshot(save)); err != nil {
		_ = save.Rollback(ctx)
		return false
	}
	return save.Commit(ctx) == nil
}
