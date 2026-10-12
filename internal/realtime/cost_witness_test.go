package realtime_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type costGatedSource struct{ entered, release chan struct{} }

func (costGatedSource) Trigger(realtime.Event) bool { return true }
func (s costGatedSource) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return realtime.Outgoing{}, context.Cause(ctx)
	case <-s.release:
		return realtime.Outgoing{}, nil
	}
}

type costGatedReader struct{ reads int }

func (r *costGatedReader) EventsAfter(_ context.Context, id kernel.ID, after int64, _ int) ([]realtime.Event, error) {
	r.reads++
	if after == 0 && r.reads > 1 {
		return []realtime.Event{{OrganizationID: id, ChannelID: kernel.ID{2}, Seq: 1, Kind: "message.posted"}}, nil
	}
	return nil, nil
}

func TestCostSynchronousRecountBlocksSameWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		hub, reader := realtime.NewHub(), &costGatedReader{}
		source := costGatedSource{make(chan struct{}), make(chan struct{})}
		sub := realtime.Subscription{Organization: kernel.ID{1}, Channel: kernel.ID{2}, Interests: []realtime.Interest{realtime.InterestMessages, realtime.InterestSidebar}}
		var frames []realtime.Outgoing
		stream := realtime.Stream{Hub: hub, Events: reader, Renderer: renderMessages{}, Authorizer: allowAll{}, Sidebar: source}
		done := make(chan error, 1)
		go func() {
			_, err := stream.Run(ctx, sub, 0, &retentionSender{send: func(out realtime.Outgoing) error { frames = append(frames, out); return nil }})
			done <- err
		}()
		<-source.entered
		hub.Raise(sub.Organization, 1)
		synctest.Wait()
		if reader.reads != 1 || len(frames) != 0 {
			t.Fatalf("blocked recount allowed writer progress: reads=%d frames=%v", reader.reads, frames)
		}
		close(source.release)
		synctest.Wait()
		if len(frames) != 2 || frames[0].Name != "sidebar" || !frames[0].Ephemeral || frames[0].ID != 0 || frames[1].Name != "message" || frames[1].ID != 1 {
			t.Fatalf("release did not unblock the same writer in order: %v", frames)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestCostFreshnessBeyondCap(t *testing.T) {
	returned, delivered := map[int64]time.Time{}, map[int64]bool{}
	for seq := int64(1); seq <= 1200; seq++ {
		returned[seq], delivered[seq] = time.Time{}, true
	}
	c := &costConnection{snapshots: []costSnapshot{{counts: [2]int64{100, 100}, through: 1199}}, sequences: delivered}
	if costComplete([]*costConnection{c}, returned) {
		t.Fatal("capped totals accepted a stale snapshot")
	}
	c.snapshots[0].through = 1200
	if !costComplete([]*costConnection{c}, returned) {
		t.Fatal("fresh capped snapshot rejected")
	}
	delete(c.sequences, 1200)
	if costComplete([]*costConnection{c}, returned) {
		t.Fatal("fresh snapshot masked a missing message")
	}
}

func TestCostWitnessUsesCountsSnapshot(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, realtime.NewHub())
	pinned, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer close(release)
	var through int64
	ctx := context.WithValue(t.Context(), costWitnessKey{}, &through)
	go func() {
		done <- platform.InSnapshot(ctx, pool, func(s platform.Snapshot) error {
			if _, err := orgpg.EventCursorIn(s).EventSeq(ctx, f.OrganizationID); err != nil {
				return err
			}
			close(pinned)
			select {
			case <-release:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
			counts, err := costPageCountsIn(s).Read(ctx, m, f.Channel.ID, []kernel.ID{f.Channel.ID}, []kernel.ID{f.Channel.DefaultTopicID}, nil)
			if err == nil && (through != 1 || counts.ChannelCounts[f.Channel.ID] != 0 || counts.TopicCounts[f.Channel.DefaultTopicID] != 0) {
				t.Error("witness or counts escaped the pinned pre-post snapshot")
			}
			return err
		})
	}()
	select {
	case <-pinned:
	case err := <-done:
		t.Fatal(err)
	}
	if _, err := posting.Post(t.Context(), m, f.Channel.ID, "committed after pin"); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := platform.InSnapshot(ctx, pool, func(s platform.Snapshot) error {
		_, err := costPageCountsIn(s).Read(ctx, m, f.Channel.ID, []kernel.ID{f.Channel.ID}, []kernel.ID{f.Channel.DefaultTopicID}, nil)
		return err
	}); err != nil || through != 2 {
		t.Fatalf("fresh snapshot: witness=%d error=%v", through, err)
	}
}
