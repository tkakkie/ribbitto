package realtime_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestCostRunnerMatchedWiringAndDrain(t *testing.T) {
	t.Setenv("RIBBITTO_STREAM_COST_WORKERS", "99")
	t.Setenv("RIBBITTO_STREAM_COST_RECOUNT_POOL", "99")
	for _, arm := range []string{"baseline", "hybrid"} {
		t.Run(arm, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				truth := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, truth, "runner", "general")
				account := identitytest.Account(t, truth, "reader@example.org", "Reader")
				member := orgtest.Member(t, truth, f.OrganizationID, account, org.RoleMember, "reader", 1)
				conversationtest.Channel(t, truth, f.OrganizationID, "zero", false)
				selected := conversationtest.Topic(t, truth, f.OrganizationID, f.Channel.ID, "Selected").ID
				members := []org.Membership{
					{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}},
					{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: member, JoinedEventSeq: 1}},
				}
				subs := []realtime.Subscription{
					{Organization: f.OrganizationID, OrganizationSlug: "runner", Account: f.AccountID, Member: f.MemberID, Channel: f.Channel.ID, Topic: &selected},
					{Organization: f.OrganizationID, OrganizationSlug: "runner", Account: account, Member: member, Channel: f.Channel.ID},
				}
				normal, separate, workers := costSizing(t, arm, 99)
				if normal != 36 || separate != 0 || workers != 26 {
					t.Fatal("matched connection/worker budget changed")
				}
				queries := &epochCounter{QueryCounter: platform.NewQueryCounter()}
				config := truth.Config()
				config.MaxConns, config.ConnConfig.Tracer = int32(normal), queries
				pool, err := pgxpool.NewWithConfig(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				r, err := newCostRunner(t.Context(), arm, pool, truth, members, subs, f.Channel.DefaultTopicID, 40*time.Millisecond, 30*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				// Compare the runner's actual closure to an explicit nil-checkpoint
				// snapshot: any oracle queries in the runner change this SQL total.
				before := queries.Counts()
				if before != (platform.QueryCounts{}) {
					t.Fatal("runner setup polluted application SQL")
				}
				_, err = costModelSnapshot(t.Context(), pool, members[0], subs[0].Channel, subs[0].Topic, nil)
				middle := queries.Counts()
				_, runnerErr := r.models[0].snapshot(t.Context())
				after := queries.Counts()
				if err != nil || runnerErr != nil || middle.Queries-before.Queries != after.Queries-middle.Queries {
					t.Fatal("runner added oracle SQL to application snapshot")
				}
				want, err := r.capture(t.Context())
				if err != nil || queries.Counts() != after || len(want[0].channels) != 2 || len(want[0].selection) != 1 || want[1].selection != nil {
					t.Fatal("shared fixture/selection or independent oracle pool lost")
				}
				q, scheduler := newCostQueue(workers, 2), realtime.NewRecountScheduler(workers, 2)
				defer q.shutdown()
				defer scheduler.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				computed, release := make(chan struct{}, 2), make(chan struct{}, 2)
				original := r.snapshot
				r.snapshot = func(ctx context.Context, m org.Membership, sub realtime.Subscription) (hybridFrame, error) {
					frame, err := original(ctx, m, sub)
					computed <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
					}
					return frame, err
				}
				done := make(chan error, 2)
				for i := range 2 {
					p := &costWriterProbe{t: t, sub: subs[i]}
					stream, sub, sender := r.wire(i, realtime.Stream{Hub: realtime.NewHub(), Events: p, Authorizer: p, Heartbeat: time.Nanosecond}, &collector{}, scheduler)
					if stream.Heartbeat != 0 || len(sub.Interests) != 2 || r.models[i].changes != r.changes || (arm == "hybrid") != (stream.Recounts == scheduler) {
						t.Fatal("matched watches/interests/heartbeat/scheduler wiring lost")
					}
					run := stream.Run
					if arm == "baseline" {
						run = (costWriter{stream, q, q.register()}).run
					}
					go func() { _, err := run(ctx, sub, r.first+int64(r.plan.capacity)-1, sender); done <- err }()
				}
				for range 2 {
					<-computed
					release <- struct{}{}
				}
				synctest.Wait()
				if costReadDrain(r.models, want) != nil {
					t.Fatal("matched initial models were not delivered")
				}
				accounting0 := r.accounting(queries)
				if accounting0.computations != 2 || accounting0.snapshots != 2 || accounting0.deltas != 0 {
					t.Fatalf("initial computation/send accounting: %+v", accounting0)
				}
				reads := r.reads(ctx, time.Now())
				if reads != (costReadResult{scheduled: 4, changed: 4}) {
					t.Fatalf("runner changed-read load incomplete: %+v", reads)
				}
				fresh, err := r.capture(t.Context())
				if err != nil || costReadDrain(r.models, fresh) == nil || sidebarOracleCompare(want[0], fresh[0]) == nil {
					t.Fatal("exact final drain accepted stale read-only state")
				}
				<-time.After(realtime.SidebarInterval)
				for range 2 {
					<-computed
					release <- struct{}{}
				}
				synctest.Wait()
				if err := costReadDrain(r.models, fresh); err != nil {
					t.Fatalf("exact final drain failed: %v", err)
				}
				if got := r.accounting(queries).since(accounting0); got.sidebarSQL <= 0 || got.otherSQL <= 0 || got.sidebarSQL+got.otherSQL != got.totalSQL || got.computations != 2 || got.snapshots != 2 || got.deltas != 0 {
					t.Fatalf("matched window included setup or lost accounting: %+v", got)
				}
				saved := r.models[1]
				r.models[1] = &costModelConnection{}
				if costReadDrain(r.models, fresh) == nil {
					t.Fatal("another stream satisfied missing final model")
				}
				r.models[1] = saved
				cancel()
				for range 2 {
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				}
				if q.stats().registered != 0 || scheduler.Stats().Registrations != 0 || len(r.senders[0].sidebars) != 2 || len(r.senders[1].sidebars) != 2 {
					t.Fatal("runner retained registrations or lost sender-side frames")
				}
				if arm == "hybrid" {
					r.hybrids[0].rejected = costHybridRejections{repair: 2, ignored: 3}
					r.hybrids[1].rejected = costHybridRejections{repair: 5, ignored: 7}
					if r.rejections() != (costHybridRejections{repair: 7, ignored: 10}) {
						t.Fatal("runner conflated repair/ignored rejections")
					}
				}
			})
		})
	}
}
