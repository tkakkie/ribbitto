package conversationpg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestPinningFlow(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "pins", "general")
	m := membership(f.OrganizationID, f.MemberID)
	m.Member.OrganizationID = f.OrganizationID
	other := m
	other.Member.ID = orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "pinner@example.org", "Curator"), org.RoleMember, "curator", 1)
	n := &recordingNotifier{t: t, pool: pool}
	p := conversationpg.NewPinning(pool, compose.PostingSequence, compose.PostingEvents, n)
	var messages []conversation.Message
	for range conversation.MaxChannelPins + 1 {
		msg, err := newPosting(pool).Post(ctx, m, f.Channel.ID, "message")
		requireNoError(t, err)
		messages = append(messages, msg)
	}
	for _, msg := range messages[:conversation.MaxChannelPins-1] {
		requireNoError(t, p.Pin(ctx, m, f.Channel.ID, msg.ID))
	}
	before := eventSeq(t, pool, f.OrganizationID)
	result := make(chan error, 2)
	start := make(chan struct{})
	for i, member := range []org.Membership{m, other} {
		go func() {
			<-start
			result <- conversationpg.NewPinning(pool, compose.PostingSequence, compose.PostingEvents, nil).Pin(ctx, member, f.Channel.ID, messages[conversation.MaxChannelPins-1+i].ID)
		}()
	}
	close(start)
	a, b := <-result, <-result
	if (a != nil || !errors.Is(b, conversation.ErrPinLimit)) && (b != nil || !errors.Is(a, conversation.ErrPinLimit)) {
		t.Fatalf("last slot: %v, %v", a, b)
	}
	state := func() (s conversation.PinState) {
		var count int
		requireNoError(t, pool.QueryRow(ctx, `SELECT pinned_by_member_id, pinned_at, (SELECT count(*) FROM message WHERE organization_id=$1 AND channel_id=$2 AND pinned_at IS NOT NULL) FROM message WHERE organization_id=$1 AND channel_id=$2 AND id=$3`, f.OrganizationID, f.Channel.ID, messages[0].ID).Scan(&s.PinnedByMemberID, &s.PinnedAt, &count))
		if count != conversation.MaxChannelPins {
			t.Fatalf("pin count %d", count)
		}
		return
	}
	original := state()
	for range 2 {
		requireNoError(t, p.Pin(ctx, other, f.Channel.ID, messages[0].ID))
	}
	if state() != original || eventSeq(t, pool, f.OrganizationID) != before+1 || len(n.raised) != conversation.MaxChannelPins-1 {
		t.Fatal("race/repeat pin changed state or consumed extra sequence")
	}
	// Appending no event fails the deferred commit guard after the metadata write.
	broken := conversationpg.NewPinning(pool, compose.PostingSequence, func(platform.Tx) conversation.EventAppender { return missingPinEvent{} }, n)
	if broken.Unpin(ctx, other, f.Channel.ID, messages[0].ID) == nil {
		t.Fatal("missing event committed")
	}
	if state() != original || eventSeq(t, pool, f.OrganizationID) != before+1 || len(n.raised) != conversation.MaxChannelPins-1 {
		t.Fatal("rollback changed metadata, consumed sequence or raised")
	}
	for range 2 {
		requireNoError(t, p.Unpin(ctx, other, f.Channel.ID, messages[0].ID))
	}
	if eventSeq(t, pool, f.OrganizationID) != before+2 || len(n.raised) != conversation.MaxChannelPins {
		t.Fatal("repeat unpin consumed sequence or raised")
	}
	requireNoError(t, p.Pin(ctx, other, f.Channel.ID, messages[0].ID))
	if renewed := state(); renewed.PinnedByMemberID != other.Member.ID || !renewed.PinnedAt.After(original.PinnedAt) {
		t.Fatalf("re-pin: %+v", renewed)
	}
	for _, raise := range n.raised {
		if raise.seq != raise.committed {
			t.Fatal("raised before commit")
		}
	}
	var valid, reads int
	requireNoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE organization_id=$1 AND kind='pins.changed' AND audience_member_id IS NULL AND data=jsonb_build_object('channel_id',$2::uuid,'message_id',(data->>'message_id')::uuid)`, f.OrganizationID, f.Channel.ID).Scan(&valid))
	requireNoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channel_read)+(SELECT count(*) FROM read_range)+(SELECT count(*) FROM topic_read_floor)`).Scan(&reads))
	if valid != conversation.MaxChannelPins+2 || reads != 0 {
		t.Fatalf("events=%d reads=%d", valid, reads)
	}
	for _, kinds := range []bool{false, true} {
		registered := eventKinds(t)
		if kinds {
			registered[conversation.KindPinsChanged] = conversationpg.PinEventKinds()[conversation.KindPinsChanged]
		}
		events, err := realtimepg.NewReader(pool, orgpg.BoundsIn, registered).EventsAfter(ctx, f.OrganizationID, before, 1)
		requireNoError(t, err)
		if len(events) != 1 || events[0].Kind != conversation.KindPinsChanged || (events[0].ChannelID == f.Channel.ID) != kinds || (events[0].Payload != nil) != kinds {
			t.Fatalf("reader activation %v: %+v", kinds, events)
		}
	}
}

type missingPinEvent struct{}

func (missingPinEvent) Append(context.Context, kernel.ID, int64, realtime.EventKind, *kernel.ID, []byte) error {
	return nil
}
