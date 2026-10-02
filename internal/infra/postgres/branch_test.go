package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
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
	posting, store, topics := postgres.NewPostingStore(pool), postgres.NewBranchStore(pool), postgres.NewTopicStore(pool)
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
	if moveKind != string(domain.EventMessagesMoved) || from != source || to != dest.ID || !slices.Equal(movedIDs, []domain.ID{posted[0].ID, posted[2].ID}) || noticeKind != string(domain.EventMessagePosted) {
		t.Fatalf("events: %s %x→%x %v, then %s", moveKind, from, to, movedIDs, noticeKind)
	}
	// Replay must preserve both durable events and their routing IDs, even
	// when the batch boundary falls between the move and its notice.
	reader := postgres.NewEventReader(pool)
	for _, limit := range []int{1, 2} {
		events, err := reader.EventsAfter(ctx, acme.OrganizationID, before, limit)
		requireNoError(t, err)
		if len(events) != limit {
			t.Fatalf("replay returned %d events, want %d", len(events), limit)
		}
		move := events[0]
		if move.OrganizationID != acme.OrganizationID || move.Seq != before+1 || move.Kind != domain.EventMessagesMoved ||
			move.ChannelID != acme.Channel.ID || move.FromTopicID != source || move.ToTopicID != dest.ID ||
			move.AudienceMemberID != nil || !slices.Equal(move.MessageIDs, movedIDs) {
			t.Fatalf("decoded move: %+v", move)
		}
		next, err := reader.EventsAfter(ctx, acme.OrganizationID, move.Seq, 1)
		requireNoError(t, err)
		if len(next) != 1 || next[0].Kind != domain.EventMessagePosted || next[0].Seq != before+2 ||
			next[0].ChannelID != acme.Channel.ID || next[0].MessageID != got[3].id || next[0].AudienceMemberID != nil {
			t.Fatalf("notice after move: %+v", next)
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
		if _, _, err := store.Branch(ctx, acme.OrganizationID, acme.Channel.ID, acme.MemberID, tt.b, notice); !errors.Is(err, tt.want) {
			t.Fatalf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	listed, err := topics.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 10)
	requireNoError(t, err)
	if eventSeq(t, pool, acme.OrganizationID) != after || len(listed) != 2 {
		t.Fatalf("a refused branch changed something: event_seq %d (was %d), topics %+v", eventSeq(t, pool, acme.OrganizationID), after, listed)
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
