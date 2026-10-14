package realtime_test

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type costCommitGate struct {
	*epochCounter
	entered chan time.Time
	release chan struct{}
}

func (g costCommitGate) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	g.epochCounter.TraceQueryEnd(ctx, conn, data)
	if p, _ := ctx.Value(costPersistKey{}).(*costPersist); p != nil && data.CommandTag.String() == "COMMIT" && data.Err == nil {
		g.entered <- p.at
		<-g.release // Hold the call after acknowledged commit, before publication/return.
	}
}

func TestCostPersistCommitBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		truth := pgtest.New(t)
		f := conversationtest.OrganizationWithOwner(t, truth, "persist", "general")
		m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
		gate := costCommitGate{&epochCounter{QueryCounter: platform.NewQueryCounter()}, make(chan time.Time), make(chan struct{})}
		config := truth.Config()
		config.ConnConfig.Tracer = gate
		pool, err := pgxpool.NewWithConfig(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		defer close(gate.release)
		r := &costRunner{pool: pool, changes: new(unread.Changes), members: []org.Membership{m}, subs: []realtime.Subscription{{Channel: f.Channel.ID}}}
		posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
		done := make(chan error, 1)
		for _, kind := range []string{"post", "read", "noop"} {
			start := time.Now()
			go func() {
				if kind == "post" {
					_, err := r.post(t.Context(), posting, m, f.Channel.ID)
					done <- err
				} else {
					r.plan, r.first = costReadPlan{streams: 1, scheduled: 1}, 2
					got := r.reads(t.Context(), time.Now())
					if got.changed != 1 && kind == "read" || got.noop != 1 && kind == "noop" {
						t.Errorf("committed read outcome: %+v", got)
					}
					done <- nil
				}
			}()
			at := <-gate.entered
			if at != start || len(done) != 0 {
				t.Fatalf("%s commit stamp missing before call return: %v want %v", kind, at, start)
			}
			<-time.After(7 * time.Second)
			gate.release <- struct{}{}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if kind != "noop" && r.changesPersisted[len(r.changesPersisted)-1].persisted != start {
				t.Fatal("persist witness used call return instead of acknowledged COMMIT")
			}
		}
		if len(r.changesPersisted) != 2 || r.changesPersisted[0].sequence != 2 || r.changesPersisted[0].stream != -1 || r.changesPersisted[1].stream != 0 || r.changesPersisted[1].readThrough != 2 {
			t.Fatal("persist witnesses lost post/read identity or included successful no-op")
		}
	})
}

func TestCostPersistRejectsUnconfirmedCommit(t *testing.T) {
	c := &epochCounter{QueryCounter: platform.NewQueryCounter()}
	for _, tt := range []struct {
		sql, tag string
		err      error
	}{
		{"commit", "ROLLBACK", nil}, {"commit", "COMMIT", errors.New("unknown outcome")}, {"rollback", "ROLLBACK", nil}, {"SELECT 1", "COMMIT", nil},
	} {
		p := new(costPersist)
		ctx := c.TraceQueryStart(context.WithValue(t.Context(), costPersistKey{}, p), nil, pgx.TraceQueryStartData{SQL: tt.sql})
		c.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag(tt.tag), Err: tt.err})
		if !p.at.IsZero() {
			t.Fatal("unconfirmed commit created persist evidence")
		}
	}
}

func TestCostSentModelPinnedReadCoverage(t *testing.T) {
	for _, arm := range []string{"baseline", "hybrid"} {
		t.Run(arm, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "sent-model", "general")
				m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
				plan, _ := newCostReadPlan(1, 1060*time.Millisecond)
				first, err := plan.seed(t.Context(), pool, m, f.Channel.ID, f.Channel.DefaultTopicID)
				if err != nil {
					t.Fatal(err)
				}
				sub := realtime.Subscription{Organization: f.OrganizationID, Member: f.MemberID, Channel: f.Channel.ID}
				changes := new(unread.Changes)
				pinned, resume, sending, sent := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
				defer close(resume)
				defer close(sent)
				c := &costModelConnection{changes: changes, sender: &retentionSender{send: func(realtime.Outgoing) error {
					sending <- struct{}{}
					<-sent
					return nil
				}}}
				c.snapshot = func(ctx context.Context) (hybridFrame, error) {
					return costModelSnapshot(ctx, pool, m, sub.Channel, nil, func(pgx.Tx) error { pinned <- struct{}{}; <-resume; return nil })
				}
				h := &costHybridConnection{costModelConnection: c, incarnation: 1, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}}
				h.Watch(sub.Organization, sub.Member)
				defer h.watch.Release()
				var source realtime.SidebarSource = c
				var sender realtime.Sender = c
				if arm == "hybrid" {
					source, sender = h, h
				}
				posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
				if _, err := posting.Post(t.Context(), m, sub.Channel, "before pin"); err != nil {
					t.Fatal(err)
				}
				for i := range 2 {
					done := make(chan error, 1)
					go func() {
						out, err := source.Recount(t.Context(), sub)
						if err == nil {
							out.Name, out.Ephemeral = "sidebar", true
							err = sender.Send(t.Context(), out)
						}
						done <- err
					}()
					<-pinned
					if i == 0 {
						if changed, err := costChangedRead(t.Context(), pool, changes, m, sub.Channel, first); err != nil || !changed {
							t.Fatalf("changed read: %t %v", changed, err)
						}
						for range 2 {
							if _, err := posting.Post(t.Context(), m, sub.Channel, "continues after read"); err != nil {
								t.Fatal(err)
							}
						}
					}
					resume <- struct{}{}
					<-sending
					if len(c.sent) != i {
						t.Fatal("computed or pending-send model became sender evidence")
					}
					<-time.After(3 * time.Second)
					at := time.Now()
					sent <- struct{}{}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					got := c.sent[i]
					want, err := sidebarOracle(t.Context(), pool, sub.Organization, sub.Member, sub.Channel, nil)
					if err != nil {
						t.Fatal(err)
					}
					if c.sentStart.Add(got.elapsed) != at || got.readThrough != int64(i)*first || got.sequence != first+106+2*int64(i) {
						t.Fatal("sender witness escaped pinned read/sequence coverage or success time")
					}
					if (got.digest == costModelDigest(want)) != (i == 1) {
						t.Fatal("sender model digest lost read exactness while posts continued")
					}
				}
				c.latest.model.channels[0].count.raw++
				if c.sent[1].digest == costModelDigest(c.latest.model) {
					t.Fatal("later model mutation rewrote sender evidence")
				}
			})
		})
	}
}

func TestCostSentModelBounded(t *testing.T) {
	if got := reflect.TypeOf(costSentModel{}).Size(); got != 64 {
		t.Fatalf("sender witness in-memory record size = %d bytes, want documented 64 bytes", got)
	}
	if costSentLimit != 1024 {
		t.Fatalf("sender witness buffer capacity = %d records, want documented 1024 records", costSentLimit)
	}
	if got := binary.Size(costSentModel{}); got != 57 {
		t.Fatalf("sender witness serialized record size = %d bytes, want documented 57 bytes", got)
	}
	c := new(costModelConnection)
	frame := hybridFrame{model: sidebarOracleModel{channels: []sidebarOracleEntry{{name: "general", count: sidebarOracleDisplay(101)}}}}
	for i := range 4 * costSentLimit {
		frame.boundary.sequence, frame.readThrough = int64(i), int64(i/2)
		c.sentModel(frame)
		if len(c.sent) != min(i+1, costSentLimit) || cap(c.sent) != costSentLimit {
			t.Fatalf("sender witness storage grew beyond bound: len=%d cap=%d limit=%d", len(c.sent), cap(c.sent), costSentLimit)
		}
	}
	if c.sentDropped != 3*costSentLimit {
		t.Fatal("sender witness overflow lost incomplete evidence")
	}
	for i, got := range c.sent {
		if got.sequence != int64(i) || got.readThrough != int64(i/2) || got.digest != costModelDigest(frame.model) || got.elapsed < 0 {
			t.Fatal("sender witness overflow overwrote first-send evidence")
		}
	}
}

func TestCostSentModelDigest(t *testing.T) {
	entry := sidebarOracleEntry{name: "general", count: sidebarOracleDisplay(101)}
	model := sidebarOracleModel{channels: []sidebarOracleEntry{entry, {name: "other"}}, topics: []sidebarOracleEntry{entry}, selection: []sidebarOracleEntry{entry}}
	c := new(costModelConnection)
	c.sentModel(hybridFrame{model: model})
	witness := c.sent[0].digest
	for _, tt := range []struct {
		name   string
		change func(*sidebarOracleModel)
	}{
		{"organization", func(m *sidebarOracleModel) { m.organization[0]++ }},
		{"member", func(m *sidebarOracleModel) { m.member[0]++ }},
		{"current", func(m *sidebarOracleModel) { m.current[0]++ }},
		{"identity", func(m *sidebarOracleModel) { m.channels[0].id[0]++ }},
		{"name", func(m *sidebarOracleModel) { m.channels[0].name += "!" }},
		{"uncapped", func(m *sidebarOracleModel) { m.channels[0].count.raw++ }},
		{"display", func(m *sidebarOracleModel) { m.channels[0].count.capped-- }},
		{"badge", func(m *sidebarOracleModel) { m.channels[0].count.badge = "100" }},
		{"order", func(m *sidebarOracleModel) { m.channels[0], m.channels[1] = m.channels[1], m.channels[0] }},
		{"topics", func(m *sidebarOracleModel) { m.topics = nil }},
		{"selection", func(m *sidebarOracleModel) { m.selection[0].count.raw++ }},
		{"list-boundary", func(m *sidebarOracleModel) { m.topics, m.selection = append(m.topics, m.selection...), nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := hybridClone(model)
			tt.change(&bad)
			if costModelDigest(bad) == witness {
				t.Fatalf("sender model digest accepted %s mismatch", tt.name)
			}
		})
	}
	model.channels[0].count.raw++
	if c.sent[0].digest != witness || costModelDigest(model) == witness {
		t.Fatal("later model mutation rewrote sender evidence")
	}
}
