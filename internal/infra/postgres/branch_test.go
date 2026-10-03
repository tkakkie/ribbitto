package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// One branch: two sequences, the move's then the notice's; the messages
// keep their id and event_seq; the notice is a message in the source topic;
// both events are logged. A stale selection changes nothing (409).
func TestBranchStore(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	random := pgtest.Channel(t, pool, acme.OrganizationID, "random", false)
	posting, store, topics := postgres.NewPostingStore(pool, postgres.EventLogIn), postgres.NewBranchStore(pool, postgres.EventLogIn), postgres.NewTopicStore(pool)
	var posted []domain.Message
	for _, body := range []string{"one", "two", "three"} {
		m, err := posting.Post(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, body)
		requireNoError(t, err)
		posted = append(posted, m)
	}
	elsewhere, err := posting.Post(ctx, acme.OrganizationID, random.ID, acme.MemberID, "elsewhere")
	requireNoError(t, err)
	before := eventSeq(t, pool, acme.OrganizationID)
	source := acme.Channel.DefaultTopicID
	notice := func(d domain.Topic) string { return "moved to " + d.Name }

	dest, seq, err := store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, topic.Branch{Messages: []domain.ID{posted[0].ID, posted[2].ID}, From: source, NewName: "design"}, notice)
	requireNoError(t, err)
	if dest.Name != "design" || dest.ChannelID != acme.Channel.ID || seq != before+2 || eventSeq(t, pool, acme.OrganizationID) != before+2 {
		t.Fatalf("branch: %+v at %d; event_seq was %d", dest, seq, before)
	}
	rows, err := pool.Query(ctx, "SELECT id, topic_id, event_seq FROM message WHERE organization_id = $1 AND channel_id = $2 ORDER BY event_seq", acme.OrganizationID, acme.Channel.ID)
	requireNoError(t, err)
	type row struct {
		id, topic domain.ID
		seq       int64
	}
	var got []row
	for rows.Next() {
		var r row
		requireNoError(t, rows.Scan(&r.id, &r.topic, &r.seq))
		got = append(got, r)
	}
	requireNoError(t, rows.Err())
	if len(got) != 4 || got[0] != (row{posted[0].ID, dest.ID, posted[0].EventSeq}) || got[1] != (row{posted[1].ID, source, posted[1].EventSeq}) ||
		got[2] != (row{posted[2].ID, dest.ID, posted[2].EventSeq}) || got[3].topic != source || got[3].seq != before+2 {
		t.Fatalf("messages after branching: %+v", got)
	}
	var body string
	requireNoError(t, pool.QueryRow(ctx, "SELECT body FROM message WHERE id = $1", got[3].id).Scan(&body))
	if body != "moved to design" {
		t.Fatalf("notice body %q", body)
	}
	var moveKind, noticeKind string
	var movedIDs []domain.ID
	var from, to domain.ID
	requireNoError(t, pool.QueryRow(ctx, `SELECT kind, (data->>'from_topic_id')::uuid, (data->>'to_topic_id')::uuid,
		ARRAY(SELECT jsonb_array_elements_text(data->'message_ids')::uuid) FROM event_log WHERE organization_id = $1 AND seq = $2`, acme.OrganizationID, before+1).Scan(&moveKind, &from, &to, &movedIDs))
	requireNoError(t, pool.QueryRow(ctx, "SELECT kind FROM event_log WHERE organization_id = $1 AND seq = $2 AND (data->>'message_id')::uuid = $3", acme.OrganizationID, before+2, got[3].id).Scan(&noticeKind))
	if moveKind != string(realtime.EventMessagesMoved) || from != source || to != dest.ID || !slices.Equal(movedIDs, []domain.ID{posted[0].ID, posted[2].ID}) || noticeKind != string(realtime.EventMessagePosted) {
		t.Fatalf("events: %s %x→%x %v, then %s", moveKind, from, to, movedIDs, noticeKind)
	}
	// Replay must preserve both durable events and their routing IDs, even
	// when the batch boundary falls between the move and its notice.
	reader := postgres.NewEventReader(pool, postgres.EventKinds())
	// Branching changes the message, never its posting-time routing data.
	for _, m := range posted {
		events, err := reader.EventsAfter(ctx, acme.OrganizationID, m.EventSeq-1, 1)
		requireNoError(t, err)
		if len(events) != 1 || !slices.Equal(events[0].Topics, []domain.ID{source}) {
			t.Fatalf("posting-time topic after branch: %+v", events)
		}
	}
	for _, limit := range []int{1, 2} {
		events, err := reader.EventsAfter(ctx, acme.OrganizationID, before, limit)
		requireNoError(t, err)
		if len(events) != limit {
			t.Fatalf("replay returned %d events, want %d", len(events), limit)
		}
		move := events[0]
		moved, err := topic.DecodeMoved(move.Payload)
		requireNoError(t, err)
		if move.OrganizationID != acme.OrganizationID || move.Seq != before+1 || move.Kind != realtime.EventMessagesMoved ||
			move.ChannelID != acme.Channel.ID || !slices.Equal(move.Topics, []domain.ID{source, dest.ID}) ||
			moved.FromTopicID != source || moved.ToTopicID != dest.ID ||
			move.AudienceMemberID != nil || !slices.Equal(moved.MessageIDs, movedIDs) {
			t.Fatalf("decoded move: %+v, %+v", move, moved)
		}
		next, err := reader.EventsAfter(ctx, acme.OrganizationID, move.Seq, 1)
		requireNoError(t, err)
		if len(next) != 1 || next[0].Kind != realtime.EventMessagePosted || next[0].Seq != before+2 ||
			next[0].ChannelID != acme.Channel.ID || next[0].AudienceMemberID != nil ||
			!slices.Equal(next[0].Topics, []domain.ID{source}) {
			t.Fatalf("notice after move: %+v", next)
		}
		notice, err := message.DecodePosted(next[0].Payload)
		if err != nil || notice.MessageID != got[3].id {
			t.Fatalf("notice payload: %+v, %v", notice, err)
		}
	}

	// Into an existing topic works the same way.
	if _, _, err := store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, topic.Branch{Messages: []domain.ID{posted[1].ID}, From: source, To: &dest.ID}, notice); err != nil {
		t.Fatalf("into an existing topic: %v", err)
	}
	after := eventSeq(t, pool, acme.OrganizationID)
	for _, tt := range []struct {
		name string
		b    topic.Branch
		want error
	}{
		// posted[0] already left the default topic: someone branched it first.
		{"stale selection", topic.Branch{Messages: []domain.ID{posted[0].ID}, From: source, NewName: "late"}, topic.ErrConflict},
		{"another channel's message", topic.Branch{Messages: []domain.ID{elsewhere.ID}, From: source, NewName: "stolen"}, topic.ErrConflict},
		{"into another channel's topic", topic.Branch{Messages: []domain.ID{posted[0].ID}, From: dest.ID, To: &random.DefaultTopicID}, topic.ErrNotFound},
		{"from another channel's topic", topic.Branch{Messages: []domain.ID{elsewhere.ID}, From: random.DefaultTopicID, To: &dest.ID}, topic.ErrNotFound},
		{"a taken name", topic.Branch{Messages: []domain.ID{posted[0].ID}, From: dest.ID, NewName: "DESIGN"}, topic.ErrNameTaken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := readBranchState(t, pool, acme.OrganizationID)
			if _, _, err := store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, tt.b, notice); !errors.Is(err, tt.want) {
				t.Fatalf("%s: %v, want %v", tt.name, err, tt.want)
			}
			assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
		})
	}
	listed, err := topics.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 10)
	requireNoError(t, err)
	if eventSeq(t, pool, acme.OrganizationID) != after || len(listed) != 2 {
		t.Fatalf("a refused branch changed something: event_seq %d (was %d), topics %+v", eventSeq(t, pool, acme.OrganizationID), after, listed)
	}
}

// failingNotice appends the move through the real appender, then fails the
// notice: the branch's last write.
type failingNotice struct {
	postgres.EventAppender
	moved *bool
}

func (f failingNotice) AppendMessagesMoved(ctx context.Context, organizationID, channelID, from, to domain.ID, messageIDs []domain.ID, seq int64) error {
	*f.moved = true
	return f.EventAppender.AppendMessagesMoved(ctx, organizationID, channelID, from, to, messageIDs, seq)
}

func (failingNotice) AppendMessagePosted(context.Context, domain.ID, domain.ID, domain.ID, domain.ID, int64) error {
	return errors.New("notice append failed")
}

type countingNotifier struct{ raised int }

func (n *countingNotifier) Raise(domain.ID, int64) { n.raised++ }

// A failure of the second append rolls back the whole branch: the move event,
// the messages' topics, the new topic, the notice and both sequence
// increments, and nothing is raised.
func TestBranchStoreFailingNoticeAppend(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	posted, err := postgres.NewPostingStore(pool, postgres.EventLogIn).Post(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, "one")
	requireNoError(t, err)
	moved := false
	events := func(tx platform.Tx) postgres.EventAppender {
		return failingNotice{EventAppender: postgres.EventLogIn(tx), moved: &moved}
	}
	notifier := &countingNotifier{}
	brancher := topic.NewBrancher(postgres.NewBranchStore(pool, events), notifier)
	member := authz.Membership{Organization: domain.Organization{ID: acme.OrganizationID}, Member: domain.Member{ID: acme.MemberID}}
	before := readBranchState(t, pool, acme.OrganizationID)
	b := topic.Branch{Messages: []domain.ID{posted.ID}, From: acme.Channel.DefaultTopicID, NewName: "design"}
	if _, err := brancher.Branch(ctx, member, acme.Channel.ID, b, func(d domain.Topic) string { return "moved to " + d.Name }); err == nil {
		t.Fatal("branch with a failing notice append succeeded")
	}
	if !moved {
		t.Fatal("the move was not appended before the notice")
	}
	assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
	if notifier.raised != 0 {
		t.Fatalf("raised %d times after a failed branch", notifier.raised)
	}
}

func TestBranchStorePartlyStaleSelection(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		name := "new topic"
		if existing {
			name = "existing topic"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pool := pgtest.New(t)
			ctx := t.Context()
			acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
			posting, store, topics := postgres.NewPostingStore(pool, postgres.EventLogIn), postgres.NewBranchStore(pool, postgres.EventLogIn), postgres.NewTopicStore(pool)
			valid, err := posting.Post(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, "still in source")
			requireNoError(t, err)
			stale, err := posting.Post(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, "already moved")
			requireNoError(t, err)
			source := acme.Channel.DefaultTopicID
			notice := func(d domain.Topic) string { return "moved to " + d.Name }
			_, _, err = store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, topic.Branch{Messages: []domain.ID{stale.ID}, From: source, NewName: "elsewhere"}, notice)
			requireNoError(t, err)

			b := topic.Branch{Messages: []domain.ID{valid.ID, stale.ID}, From: source, NewName: "destination"}
			if existing {
				dest, err := topics.CreateTopic(ctx, acme.OrganizationID, acme.Channel.ID, b.NewName)
				requireNoError(t, err)
				b.To, b.NewName = &dest.ID, ""
			}
			before := readBranchState(t, pool, acme.OrganizationID)
			// MoveMessages updates the valid row before detecting the stale
			// selection, so refusing must undo that topic_id change too.
			_, _, err = store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, b, notice)
			if !errors.Is(err, topic.ErrConflict) {
				t.Fatalf("partly stale selection: %v, want %v", err, topic.ErrConflict)
			}
			assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
		})
	}
}

type branchMessage struct {
	id, topic domain.ID
}

type branchState struct {
	messages            []branchMessage
	seq, events, topics int64
}

func readBranchState(t *testing.T, pool *pgxpool.Pool, org domain.ID) branchState {
	t.Helper()
	var state branchState
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT event_seq,
		(SELECT count(*) FROM event_log WHERE organization_id = $1),
		(SELECT count(*) FROM topic WHERE organization_id = $1)
		FROM organization WHERE id = $1`, org).Scan(&state.seq, &state.events, &state.topics))
	rows, err := pool.Query(t.Context(), "SELECT id, topic_id FROM message WHERE organization_id = $1 ORDER BY id", org)
	requireNoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var m branchMessage
		requireNoError(t, rows.Scan(&m.id, &m.topic))
		state.messages = append(state.messages, m)
	}
	requireNoError(t, rows.Err())
	return state
}

func assertBranchState(t *testing.T, got, want branchState) {
	t.Helper()
	// Comparing all message IDs also detects an unexpected branch notice.
	if !slices.Equal(got.messages, want.messages) {
		t.Errorf("messages after refusal: %+v, want %+v", got.messages, want.messages)
	}
	if got.events != want.events {
		t.Errorf("event_log rows after refusal: %d, want %d", got.events, want.events)
	}
	if got.seq != want.seq {
		t.Errorf("event_seq after refusal: %d, want %d", got.seq, want.seq)
	}
	if got.topics != want.topics {
		t.Errorf("topics after refusal: %d, want %d", got.topics, want.topics)
	}
}

func eventSeq(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org domain.ID) int64 {
	t.Helper()
	var seq int64
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id = $1", org).Scan(&seq))
	return seq
}
