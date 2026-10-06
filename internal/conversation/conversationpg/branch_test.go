package conversationpg_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

// One branch: two sequences, the move's then the notice's; the messages
// keep their id and event_seq; the notice is a message in the source topic;
// both events are logged. A stale selection changes nothing (409).
func TestBranching(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	random := conversationtest.Channel(t, pool, acme.OrganizationID, "random", false)
	notifier := &recordingNotifier{t: t, pool: pool}
	posting, brancher, member := newPosting(pool), conversationpg.NewBrancher(pool, eventSequence, appendEvents, notifier), membership(acme.OrganizationID, acme.MemberID)
	var posted []conversation.Message
	for _, body := range []string{"one", "two", "three"} {
		m, err := posting.Post(ctx, member, acme.Channel.ID, body)
		requireNoError(t, err)
		posted = append(posted, m)
	}
	elsewhere, err := posting.Post(ctx, member, random.ID, "elsewhere")
	requireNoError(t, err)
	before := eventSeq(t, pool, acme.OrganizationID)
	source := acme.Channel.DefaultTopicID
	notice := func(d conversation.Topic) string { return "moved to " + d.Name }

	dest, err := brancher.Branch(ctx, member, acme.Channel.ID, conversation.Branch{Messages: []kernel.ID{posted[0].ID, posted[2].ID}, From: source, NewName: "design"}, notice)
	requireNoError(t, err)
	// Branch returns no sequence: the notice's reaches only the notifier.
	if dest.Name != "design" || dest.ChannelID != acme.Channel.ID || !slices.Equal(notifier.raised, []raise{{acme.OrganizationID, before + 2, before + 2}}) || eventSeq(t, pool, acme.OrganizationID) != before+2 {
		t.Fatalf("branch: %+v raising %+v; event_seq was %d", dest, notifier.raised, before)
	}
	rows, err := pool.Query(ctx, "SELECT id, topic_id, event_seq FROM message WHERE organization_id = $1 AND channel_id = $2 ORDER BY event_seq", acme.OrganizationID, acme.Channel.ID)
	requireNoError(t, err)
	type row struct {
		id, topic kernel.ID
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
	var movedIDs []kernel.ID
	var from, to kernel.ID
	requireNoError(t, pool.QueryRow(ctx, `SELECT kind, (data->>'from_topic_id')::uuid, (data->>'to_topic_id')::uuid,
		ARRAY(SELECT jsonb_array_elements_text(data->'message_ids')::uuid) FROM event_log WHERE organization_id = $1 AND seq = $2`, acme.OrganizationID, before+1).Scan(&moveKind, &from, &to, &movedIDs))
	requireNoError(t, pool.QueryRow(ctx, "SELECT kind FROM event_log WHERE organization_id = $1 AND seq = $2 AND (data->>'message_id')::uuid = $3", acme.OrganizationID, before+2, got[3].id).Scan(&noticeKind))
	if moveKind != string(conversation.KindMessagesMoved) || from != source || to != dest.ID || !slices.Equal(movedIDs, []kernel.ID{posted[0].ID, posted[2].ID}) || noticeKind != string(conversation.KindPosted) {
		t.Fatalf("events: %s %x→%x %v, then %s", moveKind, from, to, movedIDs, noticeKind)
	}
	// Replay must preserve both durable events and their routing IDs, even
	// when the batch boundary falls between the move and its notice.
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t))
	// Branching changes the message, never its posting-time routing data.
	for _, m := range posted {
		events, err := reader.EventsAfter(ctx, acme.OrganizationID, m.EventSeq-1, 1)
		requireNoError(t, err)
		if len(events) != 1 || !slices.Equal(events[0].Topics, []kernel.ID{source}) {
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
		moved, err := conversation.DecodeMoved(move.Payload)
		requireNoError(t, err)
		if move.OrganizationID != acme.OrganizationID || move.Seq != before+1 || move.Kind != conversation.KindMessagesMoved ||
			move.ChannelID != acme.Channel.ID || !slices.Equal(move.Topics, []kernel.ID{source, dest.ID}) ||
			moved.FromTopicID != source || moved.ToTopicID != dest.ID ||
			move.AudienceMemberID != nil || !slices.Equal(moved.MessageIDs, movedIDs) {
			t.Fatalf("decoded move: %+v, %+v", move, moved)
		}
		next, err := reader.EventsAfter(ctx, acme.OrganizationID, move.Seq, 1)
		requireNoError(t, err)
		if len(next) != 1 || next[0].Kind != conversation.KindPosted || next[0].Seq != before+2 ||
			next[0].ChannelID != acme.Channel.ID || next[0].AudienceMemberID != nil ||
			!slices.Equal(next[0].Topics, []kernel.ID{source}) {
			t.Fatalf("notice after move: %+v", next)
		}
		notice, err := conversation.DecodePosted(next[0].Payload)
		if err != nil || notice.MessageID != got[3].id {
			t.Fatalf("notice payload: %+v, %v", notice, err)
		}
	}

	// Into an existing topic works the same way.
	if _, err := brancher.Branch(ctx, member, acme.Channel.ID, conversation.Branch{Messages: []kernel.ID{posted[1].ID}, From: source, To: &dest.ID}, notice); err != nil {
		t.Fatalf("into an existing topic: %v", err)
	}
	after := eventSeq(t, pool, acme.OrganizationID)
	for _, tt := range []struct {
		name string
		b    conversation.Branch
		want error
	}{
		// posted[0] already left the default topic: someone branched it first.
		{"stale selection", conversation.Branch{Messages: []kernel.ID{posted[0].ID}, From: source, NewName: "late"}, conversation.ErrBranchConflict},
		{"another channel's message", conversation.Branch{Messages: []kernel.ID{elsewhere.ID}, From: source, NewName: "stolen"}, conversation.ErrBranchConflict},
		{"into another channel's topic", conversation.Branch{Messages: []kernel.ID{posted[0].ID}, From: dest.ID, To: &random.DefaultTopicID}, conversation.ErrTopicNotFound},
		{"from another channel's topic", conversation.Branch{Messages: []kernel.ID{elsewhere.ID}, From: random.DefaultTopicID, To: &dest.ID}, conversation.ErrTopicNotFound},
		{"a taken name", conversation.Branch{Messages: []kernel.ID{posted[0].ID}, From: dest.ID, NewName: "DESIGN"}, conversation.ErrTopicNameTaken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := readBranchState(t, pool, acme.OrganizationID)
			if _, err := brancher.Branch(ctx, member, acme.Channel.ID, tt.b, notice); !errors.Is(err, tt.want) {
				t.Fatalf("%s: %v, want %v", tt.name, err, tt.want)
			}
			assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
		})
	}
	var listed int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM topic WHERE organization_id = $1 AND channel_id = $2", acme.OrganizationID, acme.Channel.ID).Scan(&listed))
	if eventSeq(t, pool, acme.OrganizationID) != after || listed != 2 {
		t.Fatalf("a refused branch changed something: event_seq %d (was %d), %d topics", eventSeq(t, pool, acme.OrganizationID), after, listed)
	}
}

// failingNotice appends the move through the real appender, then fails the
// notice: the branch's last write.
type failingNotice struct {
	conversation.EventAppender
	moved *bool
}

func (f failingNotice) Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	if kind == conversation.KindPosted {
		return errNoticeAppend
	}
	err := f.EventAppender.Append(ctx, organizationID, seq, kind, audience, payload)
	*f.moved = err == nil && kind == conversation.KindMessagesMoved
	return err
}

var errNoticeAppend = errors.New("notice append failed")

// recordingNotifier records each raise with the event_seq another connection
// sees then, which equals the raised sequence only after the commit.
type recordingNotifier struct {
	t      *testing.T
	pool   *pgxpool.Pool
	raised []raise
}

type raise struct {
	organizationID kernel.ID
	seq, committed int64
}

func (n *recordingNotifier) Raise(organizationID kernel.ID, seq int64) {
	n.raised = append(n.raised, raise{organizationID, seq, eventSeq(n.t, n.pool, organizationID)})
}

// A failure of the second append rolls back the whole branch: the move event,
// the messages' topics, the new topic, the notice and both sequence
// increments, and nothing is raised.
func TestBranchingFailingNoticeAppend(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	posted, err := newPosting(pool).Post(ctx, membership(acme.OrganizationID, acme.MemberID), acme.Channel.ID, "one")
	requireNoError(t, err)
	moved := false
	events := func(tx platform.Tx) conversation.EventAppender {
		return failingNotice{EventAppender: appendEvents(tx), moved: &moved}
	}
	notifier := &recordingNotifier{t: t, pool: pool}
	brancher := conversationpg.NewBrancher(pool, eventSequence, events, notifier)
	member := membership(acme.OrganizationID, acme.MemberID)
	before := readBranchState(t, pool, acme.OrganizationID)
	b := conversation.Branch{Messages: []kernel.ID{posted.ID}, From: acme.Channel.DefaultTopicID, NewName: "design"}
	if _, err := brancher.Branch(ctx, member, acme.Channel.ID, b, func(d conversation.Topic) string { return "moved to " + d.Name }); !errors.Is(err, errNoticeAppend) {
		t.Fatalf("branch = %v; want the notice append's error", err)
	}
	if !moved {
		t.Fatal("the move was not appended before the notice")
	}
	assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
	if len(notifier.raised) != 0 {
		t.Fatalf("raised %+v after a failed branch", notifier.raised)
	}
}

// A notice the database refuses stays a server error (R2 on #502): unlike
// posting, branching maps no foreign key of its insert. The membership names
// acme but globex's member, so the sequences, topics, move and moved append
// pass and the notice's insert fails on its member foreign key.
func TestBranchNoticeInsertFailure(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	globex := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	posted, err := newPosting(pool).Post(ctx, membership(acme.OrganizationID, acme.MemberID), acme.Channel.ID, "one")
	requireNoError(t, err)
	// failingNotice records the move; the notice's append is never reached.
	moved := false
	events := func(tx platform.Tx) conversation.EventAppender {
		return failingNotice{EventAppender: appendEvents(tx), moved: &moved}
	}
	notifier := &recordingNotifier{t: t, pool: pool}
	brancher := conversationpg.NewBrancher(pool, eventSequence, events, notifier)
	before := readBranchState(t, pool, acme.OrganizationID)
	b := conversation.Branch{Messages: []kernel.ID{posted.ID}, From: acme.Channel.DefaultTopicID, NewName: "design"}
	_, err = brancher.Branch(ctx, membership(acme.OrganizationID, globex.MemberID), acme.Channel.ID, b, func(d conversation.Topic) string { return "moved to " + d.Name })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "message_organization_id_member_id_fkey" || !moved {
		t.Fatalf("branch = %v, move appended %t; want the notice's member foreign key after the move", err, moved)
	}
	// Web answers these with 404, 409 or 422; anything else is a 500.
	for _, mapped := range []error{conversation.ErrTopicNotFound, conversation.ErrChannelNotFound, org.ErrNotFound,
		conversation.ErrBranchConflict, conversation.ErrInvalidBranch, conversation.ErrInvalidTopicName, conversation.ErrTopicNameTaken} {
		if errors.Is(err, mapped) {
			t.Fatalf("branch = %v; matches %v", err, mapped)
		}
	}
	assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
	if len(notifier.raised) != 0 {
		t.Fatalf("raised %+v after a failed branch", notifier.raised)
	}
}

func TestBranchingPartlyStaleSelection(t *testing.T) {
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
			acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			posting, brancher, member := newPosting(pool), conversationpg.NewBrancher(pool, eventSequence, appendEvents, nil), membership(acme.OrganizationID, acme.MemberID)
			valid, err := posting.Post(ctx, member, acme.Channel.ID, "still in source")
			requireNoError(t, err)
			stale, err := posting.Post(ctx, member, acme.Channel.ID, "already moved")
			requireNoError(t, err)
			source := acme.Channel.DefaultTopicID
			notice := func(d conversation.Topic) string { return "moved to " + d.Name }
			_, err = brancher.Branch(ctx, member, acme.Channel.ID, conversation.Branch{Messages: []kernel.ID{stale.ID}, From: source, NewName: "elsewhere"}, notice)
			requireNoError(t, err)

			b := conversation.Branch{Messages: []kernel.ID{valid.ID, stale.ID}, From: source, NewName: "destination"}
			if existing {
				dest := conversationtest.Topic(t, pool, acme.OrganizationID, acme.Channel.ID, b.NewName).ID
				b.To, b.NewName = &dest, ""
			}
			before := readBranchState(t, pool, acme.OrganizationID)
			// MoveMessages updates the valid row before detecting the stale
			// selection, so refusing must undo that topic_id change too.
			_, err = brancher.Branch(ctx, member, acme.Channel.ID, b, notice)
			if !errors.Is(err, conversation.ErrBranchConflict) {
				t.Fatalf("partly stale selection: %v, want %v", err, conversation.ErrBranchConflict)
			}
			assertBranchState(t, readBranchState(t, pool, acme.OrganizationID), before)
		})
	}
}

type branchMessage struct {
	id, topic kernel.ID
}

type branchState struct {
	messages            []branchMessage
	seq, events, topics int64
}

func readBranchState(t *testing.T, pool *pgxpool.Pool, org kernel.ID) branchState {
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
}, org kernel.ID) int64 {
	t.Helper()
	var seq int64
	requireNoError(t, pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id = $1", org).Scan(&seq))
	return seq
}
