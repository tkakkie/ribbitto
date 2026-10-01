package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// WatermarkInterval is how often Watermark checks the committed sequences:
// the longest an event committed without a Raise waits to be delivered.
const WatermarkInterval = 5 * time.Second

// SequenceReader reads organisations' committed event sequences (the
// shared-kernel organization.event_seq). infra/postgres implements it
// without importing this package.
type SequenceReader interface {
	CommittedSequences(ctx context.Context, organizations []domain.ID) (map[domain.ID]int64, error)
}

// Watermark raises the hub to each active organisation's committed
// sequence. Posting raises the hub right after its commit, but a writer
// without a notifier (cmd/seed, sign-up's member.joined) or, later, another
// process commits without one; a stream that has caught up would then wait
// until the next post. The event log stays the only truth: this only wakes
// streams, which read from their cursor.
type Watermark struct {
	Hub       *Hub
	Sequences SequenceReader
}

// Check reads, in one query, the committed sequences of the organisations
// with registered connections and raises the hub to them; Raise ignores a
// value that is not ahead. With no active organisation it reads nothing.
func (w Watermark) Check(ctx context.Context) error {
	orgs := w.Hub.ActiveOrganizations()
	if len(orgs) == 0 {
		return nil
	}
	seqs, err := w.Sequences.CommittedSequences(ctx, orgs)
	if err != nil {
		return fmt.Errorf("reading committed sequences: %w", err)
	}
	for org, seq := range seqs {
		w.Hub.Raise(org, seq)
	}
	return nil
}

// Run calls Check on every tick until ctx ends. A failed check is logged
// and retried on the next tick; streams carry on meanwhile, only without
// this safety net. ticks is a time.Ticker's channel outside tests.
func (w Watermark) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "watermark check failed", "err", err)
		}
	}
}
