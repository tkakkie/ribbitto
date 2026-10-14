package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type costWatchedModel struct {
	*costModelConnection
	probe *costWriterProbe
	watch *hybridDeliveryWatch
}

func (c *costWatchedModel) Watch(organization, member kernel.ID) realtime.SidebarWatch {
	if organization != c.probe.sub.Organization || member != c.probe.sub.Member {
		c.probe.t.Error("cost watch lost bound scope")
	}
	c.watch = &hybridDeliveryWatch{SidebarWatch: c.costModelConnection.Watch(organization, member)}
	return c.watch
}

func TestCostMatchedModel(t *testing.T) {
	for _, arm := range []string{"baseline", "scheduler"} {
		t.Run(arm, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "matched", "general")
				conversationtest.Channel(t, pool, f.OrganizationID, "zero", false)
				var selected kernel.ID
				for i := range 50 {
					selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("Topic%02d", i)).ID
				}
				m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
				exec := func(sql string, args ...any) {
					t.Helper()
					if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
						t.Fatal(err)
					}
				}
				exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
SELECT $1,$2,$3,$4,'fixture',s FROM generate_series(2,107) s`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
				exec(`UPDATE organization SET event_seq=107,event_log_boundary_seq=107 WHERE id=$1`, f.OrganizationID)
				exec(`INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)`, f.OrganizationID, f.Channel.ID, f.MemberID)
				sub := realtime.Subscription{Organization: f.OrganizationID, Member: f.MemberID, Account: f.AccountID, Channel: f.Channel.ID, OrganizationSlug: "matched", Topic: &selected, Interests: []realtime.Interest{realtime.InterestSidebar}}
				p := &costWriterProbe{t: t, sub: sub}
				changes := new(unread.Changes)
				changes.ReadChanged(sub.Organization, sub.Member) // No subscription retains this.
				pinned, resume := make(chan sidebarOracleModel, 4), make(chan struct{})
				c := &costWatchedModel{costModelConnection: &costModelConnection{changes: changes, sender: p}, probe: p}
				c.snapshot = func(ctx context.Context) (hybridFrame, error) {
					return costModelSnapshot(ctx, pool, m, sub.Channel, &selected, func(tx pgx.Tx) error {
						if c.watch == nil {
							t.Error("cost recount preceded member subscription")
						}
						want, err := sidebarOracleIn(ctx, tx, sub.Organization, sub.Member, sub.Channel, &selected)
						if err != nil {
							return err
						}
						pinned <- want
						<-resume // Uncooperative computation exposes late completion cleanup.
						return nil
					})
				}
				q, scheduler := newCostQueue(1, 1), realtime.NewRecountScheduler(1, 1)
				defer q.shutdown()
				defer scheduler.Close()
				defer close(resume)
				stream := realtime.Stream{Hub: realtime.NewHub(), Events: p, Authorizer: p, Sidebar: c}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				var work *costQueueStream
				if arm == "baseline" {
					work = q.register()
				}
				go func() {
					var err error
					if arm == "baseline" {
						_, err = (costWriter{stream, q, work}).run(ctx, sub, 107, c)
					} else {
						stream.Recounts = scheduler
						_, err = stream.Run(ctx, sub, 107, c)
					}
					done <- err
				}()
				capture := func() sidebarOracleModel {
					t.Helper()
					select {
					case want := <-pinned:
						return want
					case <-time.After(2 * realtime.SidebarInterval):
						t.Fatal("member invalidation did not start full-model recount")
						return sidebarOracleModel{}
					}
				}
				before := capture()
				synctest.Wait()
				if before.channels[0].count.raw != 106 || before.channels[0].count.capped != 100 || len(before.topics) != 50 || before.topics[49].id == selected || c.exact(before) == nil {
					t.Fatal("full-model fixture or missing-snapshot gate lost")
				}
				exec(`INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) VALUES ($1,$2,$3,0,3)`, sub.Organization, sub.Channel, sub.Member)
				changes.ReadChanged(sub.Organization, sub.Member)
				resume <- struct{}{}
				synctest.Wait()
				if err := c.exact(before); err != nil || c.latest.boundary.sequence != 107 {
					t.Fatalf("completion escaped pinned full-model boundary: %v", err)
				}
				for _, corrupt := range []func(*sidebarOracleModel){
					func(m *sidebarOracleModel) { m.member = kernel.ID{99} },
					func(m *sidebarOracleModel) { m.channels[0].count.raw++ },
					func(m *sidebarOracleModel) { m.channels[0].count.capped-- },
					func(m *sidebarOracleModel) { m.channels[1].count.badge = "1" },
					func(m *sidebarOracleModel) { m.topics[1], m.topics[2] = m.topics[2], m.topics[1] },
					func(m *sidebarOracleModel) { m.selection = nil },
				} {
					bad := hybridClone(before)
					corrupt(&bad)
					if c.exact(bad) == nil {
						t.Fatal("full-model verification accepted corrupted scope, lists or counts")
					}
				}
				fresh, err := sidebarOracle(t.Context(), pool, sub.Organization, sub.Member, sub.Channel, &selected)
				if err != nil || c.exact(fresh) == nil {
					t.Fatal("full model accepted a stale read-only change above the cap")
				}
				<-time.After(realtime.SidebarInterval)
				after := capture()
				resume <- struct{}{}
				synctest.Wait()
				if err := c.exact(after); err != nil || sidebarOracleCompare(after, fresh) != nil || len(p.frames) != 2 {
					t.Fatalf("publication during recount lost the next full model: %v", err)
				}
				changes.ReadChanged(kernel.ID{99}, sub.Member)
				changes.ReadChanged(sub.Organization, kernel.ID{99})
				<-time.After(realtime.SidebarInterval)
				synctest.Wait()
				if len(pinned) != 0 || len(p.frames) != 2 {
					t.Fatal("foreign member/organization dirtied cost model")
				}
				changes.ReadChanged(sub.Organization, sub.Member)
				capture()
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) || c.watch.releases != 1 {
					t.Fatalf("cost model exit failed: %v releases=%d", err, c.watch.releases)
				}
				watch := changes.Watch(sub.Organization, sub.Member)
				generation, _ := watch.Snapshot()
				watch.Release()
				resume <- struct{}{}
				synctest.Wait()
				if generation != 0 || len(p.frames) != 2 || (work != nil && len(work.done) != 0) || scheduler.Stats().Registrations != 0 || q.stats().registered != 0 {
					t.Fatal("disconnect retained watch, work or late full-model output")
				}
			})
		})
	}
}

func TestCostModelSendFailure(t *testing.T) {
	failure := errors.New("send failed")
	c := &costModelConnection{sender: &retentionSender{send: func(realtime.Outgoing) error { return failure }},
		snapshot: func(context.Context) (hybridFrame, error) { return hybridFrame{}, nil }}
	out, err := c.Recount(t.Context(), realtime.Subscription{})
	if err != nil {
		t.Fatal(err)
	}
	out.Name, out.Ephemeral = "sidebar", true
	if err := c.Send(t.Context(), out); !errors.Is(err, failure) || c.exact(sidebarOracleModel{}) == nil {
		t.Fatal("failed send installed full model")
	}
	c.sender = &retentionSender{send: func(realtime.Outgoing) error { return nil }}
	if err := c.Send(t.Context(), out); err != nil || c.exact(sidebarOracleModel{}) != nil {
		t.Fatal("successful send did not install full model")
	}
	if c.Send(t.Context(), out) == nil {
		t.Fatal("duplicate full-model completion accepted")
	}
}
