package conversationpg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestResolutionFlow(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "resolution", "general")
	m := membership(f.OrganizationID, f.MemberID)
	m.Member.OrganizationID = f.OrganizationID
	topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "decision").ID
	var messages []conversation.Message
	for range 2 {
		msg, err := newPosting(pool).PostToTopic(ctx, m, f.Channel.ID, &topic, "choice")
		requireNoError(t, err)
		messages = append(messages, msg)
	}
	n := &recordingNotifier{t: t, pool: pool}
	r := conversationpg.NewResolution(pool, compose.PostingSequence, compose.PostingEvents, n)
	before := eventSeq(t, pool, f.OrganizationID)
	state := func() (id kernel.ID, version string) {
		requireNoError(t, pool.QueryRow(ctx, `SELECT coalesce(conclusion_message_id,'00000000-0000-0000-0000-000000000000'),xmin::text FROM topic WHERE id=$1`, topic).Scan(&id, &version))
		return
	}
	for i, op := range []func() error{
		func() error { return r.Resolve(ctx, m, f.Channel.ID, topic, messages[0].ID) },
		func() error { return r.Resolve(ctx, m, f.Channel.ID, topic, messages[1].ID) },
		func() error { return r.Reopen(ctx, m, f.Channel.ID, topic) },
	} {
		requireNoError(t, op())
		id, version := state()
		want := kernel.ID{}
		if i < 2 {
			want = messages[i].ID
		}
		if id != want {
			t.Fatalf("conclusion %v, want %v", id, want)
		}
		requireNoError(t, op())
		if again, after := state(); again != id || after != version || eventSeq(t, pool, f.OrganizationID) != before+int64(i)+1 || len(n.raised) != i+1 {
			t.Fatal("no-op wrote state, emitted an event, consumed a sequence or raised")
		}
	}
	for _, raise := range n.raised {
		if raise.seq != raise.committed {
			t.Fatal("raised before commit")
		}
	}
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	stale := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "stale").ID
	foreign, err := newPosting(pool).Post(ctx, m, other.ID, "elsewhere")
	requireNoError(t, err)
	for _, tc := range []struct {
		name           string
		m              org.Membership
		topic, message kernel.ID
		want           error
	}{
		{"nonmember", org.Membership{}, topic, messages[0].ID, org.ErrNotFound},
		{"mismatched membership", membership(f.OrganizationID, f.MemberID), topic, messages[0].ID, org.ErrNotFound},
		{"default", m, f.Channel.DefaultTopicID, messages[0].ID, conversation.ErrDefaultTopicResolution},
		{"missing topic", m, kernel.ID{}, messages[0].ID, conversation.ErrTopicNotFound},
		{"other channel topic", m, other.DefaultTopicID, messages[0].ID, conversation.ErrTopicNotFound},
		{"missing message", m, topic, kernel.ID{}, conversation.ErrMessageNotFound},
		{"other channel message", m, topic, foreign.ID, conversation.ErrMessageNotFound},
		{"wrong topic", m, stale, messages[0].ID, conversation.ErrConclusionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := readBranchState(t, pool, f.OrganizationID)
			if err := r.Resolve(ctx, tc.m, f.Channel.ID, tc.topic, tc.message); !errors.Is(err, tc.want) {
				t.Fatalf("Resolve = %v, want %v", err, tc.want)
			}
			assertBranchState(t, readBranchState(t, pool, f.OrganizationID), before)
		})
	}
	requireNoError(t, r.Resolve(ctx, m, f.Channel.ID, topic, messages[0].ID))
	broken := conversationpg.NewResolution(pool, compose.PostingSequence, func(platform.Tx) conversation.EventAppender { return missingPinEvent{} }, n)
	beforeFailure := readBranchState(t, pool, f.OrganizationID)
	id, version := state()
	if broken.Reopen(ctx, m, f.Channel.ID, topic) == nil {
		t.Fatal("state committed without its event")
	}
	assertBranchState(t, readBranchState(t, pool, f.OrganizationID), beforeFailure)
	if after, v := state(); after != id || v != version || len(n.raised) != 4 {
		t.Fatal("failed append changed conclusion or notified")
	}
	branch := conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, n)
	for i, msg := range []conversation.Message{messages[1], messages[0]} {
		prev := eventSeq(t, pool, f.OrganizationID)
		_, err := branch.Branch(ctx, m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{msg.ID}, From: topic, To: &f.Channel.DefaultTopicID}, func(conversation.Topic) string { return "moved" })
		requireNoError(t, err)
		if id, _ := state(); (id == (kernel.ID{})) != (i == 1) || eventSeq(t, pool, f.OrganizationID) != prev+2+int64(i) {
			t.Fatal("branch failed to preserve/clear conclusion with exactly the required events")
		}
	}
	events, err := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t)).EventsAfter(ctx, f.OrganizationID, before, 20)
	requireNoError(t, err)
	var changes int
	for _, event := range events {
		if event.Kind != conversation.KindResolutionChanged {
			continue
		}
		changes++
		channel, source, err := conversation.DecodeResolutionChanged(event.Payload)
		if err != nil || source != topic || channel != f.Channel.ID || event.ChannelID != channel || event.Topics != nil || event.AudienceMemberID != nil {
			t.Fatalf("invalid replay routing: %+v, %v", event, err)
		}
	}
	if changes != 5 {
		t.Fatalf("resolution events %d, want 5", changes)
	}
}

func TestResolutionBranchRollback(t *testing.T) {
	for _, fail := range []string{"append", "state"} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			pool := pgtest.New(t)
			ctx := t.Context()
			f := conversationtest.OrganizationWithOwner(t, pool, "rollback", "general")
			m := membership(f.OrganizationID, f.MemberID)
			m.Member.OrganizationID = f.OrganizationID
			topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "decision").ID
			msg, err := newPosting(pool).PostToTopic(ctx, m, f.Channel.ID, &topic, "conclusion")
			requireNoError(t, err)
			requireNoError(t, conversationpg.NewResolution(pool, compose.PostingSequence, compose.PostingEvents, nil).Resolve(ctx, m, f.Channel.ID, topic, msg.ID))
			events := compose.PostingEvents
			failure := errors.New("resolution append failed")
			if fail == "append" {
				events = func(tx platform.Tx) conversation.EventAppender {
					return resolutionAppendFailure{compose.PostingEvents(tx), failure}
				}
			} else {
				// The move and notice events have already been appended when the clear fails.
				_, err := pool.Exec(ctx, `CREATE FUNCTION reject_conclusion_clear() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'clear refused'; END $$;
				CREATE TRIGGER reject_conclusion_clear BEFORE UPDATE OF conclusion_message_id ON topic FOR EACH ROW EXECUTE FUNCTION reject_conclusion_clear()`)
				requireNoError(t, err)
			}
			n := &recordingNotifier{t: t, pool: pool}
			before := readBranchState(t, pool, f.OrganizationID)
			_, err = conversationpg.NewBrancher(pool, compose.PostingSequence, events, compose.BranchReads, n).Branch(ctx, m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{msg.ID}, From: topic, NewName: "destination"}, func(conversation.Topic) string { return "moved" })
			if err == nil {
				t.Fatal("failed branch committed")
			}
			var pgErr *pgconn.PgError
			if fail == "append" && !errors.Is(err, failure) || fail == "state" && (!errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "clear refused") {
				t.Fatalf("unexpected failure: %v", err)
			}
			assertBranchState(t, readBranchState(t, pool, f.OrganizationID), before)
			got, err := conversationpg.NewTopics(pool).Get(ctx, m, f.Channel.ID, topic)
			requireNoError(t, err)
			if got.ConclusionMessageID != msg.ID || len(n.raised) != 0 {
				t.Fatal("rollback changed resolution or notified")
			}
		})
	}
}

// resolutionAppendFailure fails only after branching has cleared the conclusion.
type resolutionAppendFailure struct {
	conversation.EventAppender
	failure error
}

func (f resolutionAppendFailure) Append(ctx context.Context, o kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	if kind == conversation.KindResolutionChanged {
		return f.failure
	}
	return f.EventAppender.Append(ctx, o, seq, kind, audience, data)
}
