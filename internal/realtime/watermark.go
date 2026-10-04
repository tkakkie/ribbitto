package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// WatermarkInterval is how often Watermark checks the committed sequences.
// An event committed without a Raise is delivered after the next successful
// check, so it waits about this long, plus any failed checks before it.
const WatermarkInterval = 5 * time.Second

// watermarkCheckTimeout bounds one check's query, so a stalled one cannot
// hold a pool connection or delay the following checks.
const watermarkCheckTimeout = 2 * time.Second

// SequenceReader reads organisations' committed event sequences (org's
// organization.event_seq). infra/postgres implements it until org's
// columns move (step 3).
type SequenceReader interface {
	CommittedSequences(ctx context.Context, organizations []kernel.ID) (map[kernel.ID]int64, error)
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
	// Timeout bounds each check; zero means watermarkCheckTimeout.
	Timeout time.Duration
}

// Check reads, in one query, the committed sequences of the organisations
// with registered connections and raises the hub to those still active; a
// value that is not ahead changes nothing. With no active organisation it
// reads nothing.
func (w Watermark) Check(ctx context.Context) error {
	orgs := w.Hub.ActiveOrganizations()
	if len(orgs) == 0 {
		return nil
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = watermarkCheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	seqs, err := w.Sequences.CommittedSequences(ctx, orgs)
	if err != nil {
		return fmt.Errorf("reading committed sequences: %w", err)
	}
	for org, seq := range seqs {
		w.Hub.RaiseIfActive(org, seq)
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
		// A tick and the end of ctx can be ready together; never start a
		// check after stop.
		if ctx.Err() != nil {
			return
		}
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "watermark check failed", "err", err)
		}
	}
}
