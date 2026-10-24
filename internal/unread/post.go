package unread

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/conversation"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PostReads adds only the author's own message to their read set.
type PostReads struct {
	ranges RangeWriterIn
}

// NewPostReads builds post reads with a transaction-bound range writer.
func NewPostReads(ranges RangeWriterIn) *PostReads {
	return &PostReads{ranges: ranges}
}

// Read retains posting's organisation-first lock order and never completes tx.
// The caller supplies resolved scope, join sequence and the own-message bounds.
func (w *PostReads) Read(ctx context.Context, tx platform.Tx, scope Scope, joined int64, own conversation.SequenceRange) error {
	if err := w.ranges(tx).Merge(ctx, scope, joined, own); err != nil {
		return fmt.Errorf("reading own post: %w", err)
	}
	return nil
}
