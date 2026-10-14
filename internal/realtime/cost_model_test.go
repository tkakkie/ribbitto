package realtime_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

// Both comparison arms use this full-model source and single-writer sink.
// Tokens keep unexported model witnesses out of the simulated SSE payload.
// The historical stage-1 two-count source remains separate from #952.
type costModelConnection struct {
	changes      *unread.Changes
	snapshot     func(context.Context) (hybridFrame, error)
	sender       realtime.Sender
	mu           sync.Mutex
	next         uint64
	pending      map[string]hybridFrame
	latest       hybridFrame
	computations atomic.Int64
}

func (c *costModelConnection) Watch(organization, member kernel.ID) realtime.SidebarWatch {
	return c.changes.Watch(organization, member)
}

func (c *costModelConnection) Trigger(e realtime.Event) bool {
	return (&costConnection{}).Trigger(e)
}

func (c *costModelConnection) Recount(ctx context.Context, sub realtime.Subscription) (realtime.Outgoing, error) {
	// Count actual starts, including errors, cancellation and rejected output.
	c.computations.Add(1)
	frame, err := c.snapshot(context.WithValue(ctx, costRoleKey{}, costSidebar))
	if err != nil {
		return realtime.Outgoing{}, err
	}
	if frame.model.organization != sub.Organization || frame.model.member != sub.Member || frame.model.current != sub.Channel {
		return realtime.Outgoing{}, fmt.Errorf("full-model recount lost bound scope")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	frame.request = c.next
	token := fmt.Sprint(c.next)
	// Each stream has one active recount. Cancelled streams discard this sink;
	// successful delivery consumes the witness before requesting another one.
	c.pending = map[string]hybridFrame{token: frame}
	return realtime.Outgoing{Data: []byte(token)}, nil
}

func (c *costModelConnection) Send(ctx context.Context, out realtime.Outgoing) error {
	var frame hybridFrame
	if out.Name == "sidebar" {
		c.mu.Lock()
		var ok bool
		frame, ok = c.pending[string(out.Data)]
		valid := ok && frame.request > c.latest.request && out.ID == 0 && out.Ephemeral
		c.mu.Unlock()
		if !valid {
			return fmt.Errorf("invalid full-model completion")
		}
	}
	if err := c.sender.Send(ctx, out); err != nil {
		return err
	}
	if out.Name == "sidebar" {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.latest = frame
		delete(c.pending, string(out.Data))
	}
	return nil
}

func (c *costModelConnection) Heartbeat(ctx context.Context) error { return c.sender.Heartbeat(ctx) }

func (c *costModelConnection) exact(want sidebarOracleModel) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest.request == 0 {
		return fmt.Errorf("missing full-model snapshot")
	}
	return sidebarOracleCompare(c.latest.model, want)
}

// All application SQL, including uncapped counts and sequence coverage, shares
// one pinned snapshot and is counted by the caller's instrumented pool. The
// optional checkpoint is for deterministic verification only; runners pass nil.
func costModelSnapshot(ctx context.Context, pool *pgxpool.Pool, m org.Membership, channel kernel.ID, selected *kernel.ID, checkpoint func(pgx.Tx) error) (hybridFrame, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hybridFrame{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var frame hybridFrame
	if err := tx.QueryRow(ctx, `SELECT event_seq FROM organization WHERE id=$1`, m.Organization.ID).Scan(&frame.boundary.sequence); err != nil {
		return frame, err
	}
	if checkpoint != nil {
		if err := checkpoint(tx); err != nil {
			return frame, err
		}
	}
	frame.model, err = hybridSnapshotIn(ctx, tx, m, channel, selected)
	if err != nil {
		return frame, err
	}
	return frame, tx.Commit(ctx)
}
