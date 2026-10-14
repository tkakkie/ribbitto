package realtime_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestCostAccountingComputationOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		truth := pgtest.New(t)
		f := conversationtest.OrganizationWithOwner(t, truth, "accounting", "general")
		m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
		plan, err := newCostReadPlan(1, 10*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		first, err := plan.seed(t.Context(), truth, m, f.Channel.ID, f.Channel.DefaultTopicID)
		if err != nil {
			t.Fatal(err)
		}
		queries := &epochCounter{QueryCounter: platform.NewQueryCounter()}
		config := truth.Config()
		config.ConnConfig.Tracer = queries
		pool, err := pgxpool.NewWithConfig(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		sub := realtime.Subscription{Organization: f.OrganizationID, Member: f.MemberID, Channel: f.Channel.ID}
		failSend, failCompute, cancelCompute := false, false, false
		sink := &costRunnerSender{Sender: &retentionSender{send: func(realtime.Outgoing) error {
			if failSend {
				return errors.New("send failed")
			}
			return nil
		}}}
		c := &costModelConnection{changes: new(unread.Changes), sender: sink}
		c.snapshot = func(ctx context.Context) (hybridFrame, error) {
			if ctx.Err() != nil {
				return hybridFrame{}, context.Cause(ctx)
			}
			if failCompute || cancelCompute {
				tx, err := pool.Begin(ctx)
				if err != nil {
					return hybridFrame{}, err
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if cancelCompute {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					_, err = tx.Exec(cancelled, "SELECT 1")
					return hybridFrame{}, err
				}
				_, err = tx.Exec(ctx, "SELECT 1/0")
				return hybridFrame{}, err
			}
			return costModelSnapshot(ctx, pool, m, sub.Channel, nil, nil)
		}
		h := &costHybridConnection{costModelConnection: c, incarnation: 1, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}}
		h.Watch(sub.Organization, sub.Member)
		defer h.watch.Release()
		r := &costRunner{models: []*costModelConnection{c}, senders: []*costRunnerSender{sink}, hybrids: []*costHybridConnection{h}}
		for _, outcome := range []string{"applied", "repair", "ignored", "failed-send", "failed-computation", "cancelled-computation"} {
			func() {
				before := r.accounting(queries)
				sentBefore := len(c.sent)
				failSend, failCompute = outcome == "failed-send", outcome == "failed-computation"
				cancelCompute = outcome == "cancelled-computation"
				out, computeErr := h.Recount(t.Context(), sub)
				// A computed witness is not yet a send, even when it will be accepted.
				computed := r.accounting(queries).since(before)
				if computed.computations != 1 || computed.snapshots != 0 || computed.deltas != 0 {
					t.Fatalf("computation counted as delivery: %+v", computed)
				}
				if failCompute || cancelCompute {
					if computeErr == nil || computed != (costAccounting{sidebarSQL: 3, totalSQL: 3, computations: 1}) { // BEGIN, failed query, ROLLBACK.
						t.Fatalf("%s lost attempted SQL: %+v err=%v", outcome, computed, computeErr)
					}
					if cancelCompute && !errors.Is(computeErr, context.Canceled) {
						t.Fatalf("cancelled query: %v", computeErr)
					}
					return
				}
				if computeErr != nil || computed != (costAccounting{sidebarSQL: 7, totalSQL: 7, computations: 1}) { // Five queries, BEGIN and COMMIT.
					t.Fatalf("snapshot SQL accounting: %+v err=%v", computed, computeErr)
				}
				frame := c.pending[string(out.Data)]
				switch outcome {
				case "repair":
					frame.boundary.sequence--
				case "ignored":
					frame.incarnation = 0
				}
				c.pending[string(out.Data)] = frame
				out.Name, out.Ephemeral = "sidebar", true
				err := h.Send(t.Context(), out)
				want := costAccounting{sidebarSQL: 7, totalSQL: 7, computations: 1}
				if outcome == "applied" {
					want.snapshots = 1
				}
				if n := len(c.sent) - sentBefore; int64(n) != want.snapshots {
					t.Fatalf("%s produced %d sender witnesses; want %d", outcome, n, want.snapshots)
				}
				if got := r.accounting(queries).since(before); got != want || (err != nil) != failSend {
					t.Fatalf("%s accounting: got %+v want %+v err=%v", outcome, got, want, err)
				}
			}()
		}
		beforeCancel := r.accounting(queries)
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := h.Recount(cancelled, sub); !errors.Is(err, context.Canceled) || r.accounting(queries).since(beforeCancel) != (costAccounting{computations: 1}) {
			t.Fatal("cancelled computation was omitted or counted as a send")
		}
		before := r.accounting(queries)
		if _, err := sidebarOracle(t.Context(), truth, sub.Organization, sub.Member, sub.Channel, nil); err != nil {
			t.Fatal(err)
		}
		if got := r.accounting(queries).since(before); got != (costAccounting{}) {
			t.Fatalf("oracle polluted application accounting: %+v", got)
		}
		before = r.accounting(queries)
		if changed, err := costChangedRead(t.Context(), pool, c.changes, m, sub.Channel, first); err != nil || !changed {
			t.Fatalf("changed read: changed=%t err=%v", changed, err)
		}
		if got := r.accounting(queries).since(before); got.sidebarSQL != 0 || got.otherSQL <= 0 || got != (costAccounting{otherSQL: got.totalSQL, totalSQL: got.totalSQL}) {
			t.Fatalf("changed read SQL attributed to sidebar or lost from application total: %+v", got)
		}
	})
}

// Synthetic delta frames exercise classification only; the production-only
// arm cannot derive these effects and never emits this frame kind.
func TestCostAccountingHypotheticalSender(t *testing.T) {
	fail := false
	s := &costRunnerSender{Sender: &retentionSender{send: func(realtime.Outgoing) error {
		if fail {
			return errors.New("send failed")
		}
		return nil
	}}}
	for _, name := range []string{"sidebar", "sidebar-delta", "message", "heartbeat", "sidebar-delta"} {
		if err := s.Send(t.Context(), realtime.Outgoing{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	fail = true
	for _, name := range []string{"sidebar", "sidebar-delta"} {
		if s.Send(t.Context(), realtime.Outgoing{Name: name}) == nil {
			t.Fatal("failed send unexpectedly succeeded")
		}
	}
	if s.snapshots.Load() != 1 || s.deltas.Load() != 2 {
		t.Fatal("sender conflated snapshots/deltas, messages/heartbeats or failed sends")
	}
}
