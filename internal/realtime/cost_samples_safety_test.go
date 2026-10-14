package realtime_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestCostSamplesExactAndIncomplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		channel, topic, other := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
		model := func(n int64) sidebarOracleModel {
			return sidebarOracleModel{current: channel, channels: []sidebarOracleEntry{{id: channel, count: sidebarOracleDisplay(n)}, {id: other, count: sidebarOracleDisplay(1)}}, topics: []sidebarOracleEntry{{id: topic, count: sidebarOracleDisplay(n)}}, selection: []sidebarOracleEntry{{id: topic, count: sidebarOracleDisplay(n)}}}
		}
		c := &costModelConnection{sentStart: start}
		r := &costRunner{models: []*costModelConnection{c}, members: []org.Membership{{Member: org.Member{JoinedEventSeq: 1}}}, subs: []realtime.Subscription{{Channel: channel}}, topic: topic, initial: []sidebarOracleModel{model(2)},
			history:          []costExpectedMessage{{channel, topic, 2}, {other, other, 3}, {channel, topic, 4}},
			changesPersisted: []costChange{{stream: 0, readThrough: 2, persisted: start.Add(time.Second)}, {stream: -1, sequence: 5, persisted: start.Add(2 * time.Second)}, {stream: -1, sequence: 6, persisted: start.Add(3 * time.Second)}, {stream: 0, readThrough: 4, persisted: start.Add(4 * time.Second)}, {stream: 0, readThrough: 6, persisted: start.Add(5 * time.Second)}, {stream: 0, readThrough: 2}}}
		// Valid old coverage, then invalid digest at new coverage: neither ends
		// the read sample. A later exact frame must not replace the first one.
		for _, w := range []struct {
			at, seq, read, count int64
			kind                 costExactKind
		}{{2, 4, 0, 2, costNormal}, {3, 4, 2, 2, costNormal}, {4, 4, 2, 1, costNormal}, {5, 5, 2, 2, costNormal}, {6, 6, 2, 3, costDelta}, {7, 6, 4, 2, costPeriodic}, {12, 6, 6, 0, costPeriodic}} {
			c.sent = append(c.sent, costSentModel{elapsed: time.Duration(w.at) * time.Second, sequence: w.seq, readThrough: w.read, digest: costModelDigest(model(w.count)), kind: w.kind})
		}
		end := start.Add(10 * time.Second)
		want := costSamples{total: 6, incomplete: 2, byKind: [3][]time.Duration{{3 * time.Second, 3 * time.Second}, {3 * time.Second}, {3 * time.Second}}}
		if got := r.samples(start, end); !reflect.DeepEqual(got, want) {
			t.Fatalf("first exact coverage/digest/category or incomplete samples: got %s want %s", got, want)
		}
		c.sentDropped = 1
		if got := r.samples(start, end); got.total != 6 || got.incomplete != 6 || got.overflowedStreams != 1 || len(got.byKind[0])+len(got.byKind[1])+len(got.byKind[2]) != 0 {
			t.Fatalf("overflow treated truncated evidence as complete: %s", got)
		}
		c.sentDropped = 0
		// A post affects every stream; a changed read only its bound stream.
		r.models = append(r.models, &costModelConnection{sentStart: start})
		r.initial = append(r.initial, model(2))
		r.members = append(r.members, r.members[0])
		if got := r.samples(start, end); got.total != 8 || got.incomplete != 4 {
			t.Fatalf("missing stream lost post samples or borrowed read evidence: %s", got)
		}
	})
}

func TestCostSamplesAttributionBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, arm := range []string{"baseline", "hybrid"} {
			c := &costModelConnection{changes: new(unread.Changes), sender: &retentionSender{send: func(realtime.Outgoing) error { return nil }}}
			entered, release := make(chan struct{}), make(chan struct{})
			c.snapshot = func(_ context.Context) (hybridFrame, error) { close(entered); <-release; return hybridFrame{}, nil }
			h := &costHybridConnection{costModelConnection: c, incarnation: 1, coverage: &hybridCoverage{frame: hybridFrame{incarnation: 1}}, interval: 10 * time.Second}
			h.Watch(kernel.ID{}, kernel.ID{})
			defer h.watch.Release()
			source := realtime.SidebarSource(c)
			if arm == "hybrid" {
				source = h
			}
			done := make(chan realtime.Outgoing, 1)
			go func() {
				out, err := source.Recount(t.Context(), realtime.Subscription{})
				if err != nil {
					t.Error(err)
				}
				done <- out
			}()
			<-entered
			<-time.After(10 * time.Second)
			synctest.Wait()
			close(release)
			out := <-done
			out.Name, out.Ephemeral = "sidebar", true
			sender := realtime.Sender(c)
			if arm == "hybrid" {
				sender = h
			}
			if err := sender.Send(t.Context(), out); err != nil {
				t.Fatal(err)
			}
			if c.sent[0].kind != costNormal {
				t.Fatal("periodic tick relabelled an already-started normal recount")
			}
			c.snapshot = func(_ context.Context) (hybridFrame, error) { return hybridFrame{}, nil }
			out, err := source.Recount(t.Context(), realtime.Subscription{})
			if err != nil {
				t.Fatal(err)
			}
			out.Name, out.Ephemeral = "sidebar", true
			if err := sender.Send(t.Context(), out); err != nil {
				t.Fatal(err)
			}
			want := costNormal
			if arm == "hybrid" {
				want = costPeriodic
			}
			if c.sent[1].kind != want {
				t.Fatalf("%s lost computation-start periodic attribution: got %d want %d", arm, c.sent[1].kind, want)
			}
			h.watch.Release()
		}
	})
}

func TestCostSamplesZeroDeltaReport(t *testing.T) {
	s := costSamples{total: 1, incomplete: 1}
	if got := s.categoryReport(costDelta, "delta"); !strings.Contains(got, "category=delta samples=0") || !strings.Contains(got, "evidence=false") {
		t.Fatalf("zero delta samples counted as passing evidence: %s", got)
	}
}
