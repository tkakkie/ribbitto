package postgres_test

import (
	"context"
	"errors"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"reflect"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestEventsAfter(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "events", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	// Insert out of sequence, including a future payload the reader cannot decode.
	_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 3, 'future.private', $2, '{"channel_id":"future-format"}')`, f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	posted, err := newPosting(pool).Post(ctx, membership(f), f.Channel.ID, "hello")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data)
		VALUES ($1, 1, 'member.joined', jsonb_build_object('member_id', $2::uuid))`, f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	_, err = newPosting(pool).Post(ctx, membership(other), other.Channel.ID, "other org")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_seq = 3 WHERE id = $1", f.OrganizationID)
	requireNoError(t, err)
	stored := func(seq int64) []byte {
		var data []byte
		requireNoError(t, pool.QueryRow(ctx, "SELECT data FROM event_log WHERE organization_id = $1 AND seq = $2", f.OrganizationID, seq).Scan(&data))
		return data
	}
	want := []realtime.Event{
		{OrganizationID: f.OrganizationID, Seq: 1, Kind: org.KindJoined, Payload: stored(1)},
		{OrganizationID: f.OrganizationID, Seq: posted.EventSeq, Kind: conversation.KindPosted, ChannelID: f.Channel.ID,
			Topics: []kernel.ID{posted.TopicID}, Payload: stored(posted.EventSeq)},
		{OrganizationID: f.OrganizationID, Seq: 3, Kind: "future.private", AudienceMemberID: &f.MemberID},
	}
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t))
	for _, tt := range []struct {
		name  string
		after int64
		limit int
		want  []realtime.Event
	}{
		{"ordered and scoped envelopes and IDs", 0, 10, want},
		{"limited", 0, 2, want[:2]},
		{"exclusive cursor", 1, 1, want[1:2]},
		{"next batch includes unknown kind", 2, 2, want[2:]},
		{"caught up", 3, 10, []realtime.Event{}},
		{"zero limit", 0, 0, []realtime.Event{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reader.EventsAfter(ctx, f.OrganizationID, tt.after, tt.limit)
			requireNoError(t, err)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("EventsAfter: %+v; want %+v", got, tt.want)
			}
		})
	}
	// The payloads still name the member and the message.
	got, err := reader.EventsAfter(ctx, f.OrganizationID, 0, 2)
	requireNoError(t, err)
	if joined, err := org.DecodeJoined(got[0].Payload); err != nil || joined.MemberID != f.MemberID {
		t.Fatalf("joined payload: %+v, %v; want member %v", joined, err, f.MemberID)
	}
	if p, err := conversation.DecodePosted(got[1].Payload); err != nil || p.MessageID != posted.ID || p.TopicID == nil || *p.TopicID != posted.TopicID {
		t.Fatalf("posted payload: %+v, %v; want message %v in topic %v", p, err, posted.ID, posted.TopicID)
	}
	// A known event can also target a single member; audience is not payload data.
	_, err = pool.Exec(ctx, "UPDATE event_log SET audience_member_id = $2 WHERE organization_id = $1 AND seq = 2", f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	want[1].AudienceMemberID = &f.MemberID
	got, err = reader.EventsAfter(ctx, f.OrganizationID, 1, 1)
	requireNoError(t, err)
	if !reflect.DeepEqual(got, want[1:2]) {
		t.Fatalf("targeted event: %+v; want %+v", got, want[1:2])
	}
}

// Malformed data of a known kind fails the whole batch, including the valid
// event before it: no partial batch reaches a stream. The codecs' own tests
// cover which payloads are malformed.
func TestEventsAfterMalformedData(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	org := orgtest.Organization(t, pool, "malformed", "Malformed", 2)
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t))
	_, err := pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
		VALUES ($1, 1, 'member.joined', '{"member_id":"00000000-0000-0000-0000-000000000001"}')`, org)
	requireNoError(t, err)
	for _, kind := range []string{"message.posted", "member.joined", "messages.moved"} {
		t.Run(kind, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
				VALUES ($1, 2, $2, '{}') ON CONFLICT (organization_id, seq) DO UPDATE SET kind = EXCLUDED.kind, data = EXCLUDED.data`, org, kind)
			requireNoError(t, err)
			if events, err := reader.EventsAfter(t.Context(), org, 0, 10); err == nil || len(events) != 0 {
				t.Fatalf("malformed event after a valid one: %+v, %v; want error without events", events, err)
			}
		})
	}
}

func TestEventsAfterCursorAboveLog(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "cursor", "general")
	posted, err := newPosting(pool).Post(ctx, membership(f), f.Channel.ID, "hello")
	requireNoError(t, err)
	empty := orgtest.Organization(t, pool, "empty", "Empty", 0)
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t))
	for _, tt := range []struct {
		name string
		org  kernel.ID
		seq  int64
	}{
		{"populated log", f.OrganizationID, posted.EventSeq},
		{"empty log", empty, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, limit := range []int{0, 10} {
				for _, after := range []int64{tt.seq + 1, tt.seq + 10, 1<<63 - 1} {
					got, err := reader.EventsAfter(ctx, tt.org, after, limit)
					if !errors.Is(err, realtime.ErrCursorExpired) || len(got) != 0 {
						t.Errorf("after=%d limit=%d: %v, %v; want no events and ErrCursorExpired", after, limit, got, err)
					}
				}
				got, err := reader.EventsAfter(ctx, tt.org, tt.seq, limit)
				if err != nil || len(got) != 0 {
					t.Errorf("at event_seq=%d limit=%d: %v, %v; want empty batch without error", tt.seq, limit, got, err)
				}
			}
		})
	}
}

// The reader routes every registered kind through its Router, with no
// built-in kind branch; an unregistered kind keeps only its envelope, even a
// built-in one with malformed data, and a Router's error fails the whole batch.
func TestEventsAfterRegisteredKinds(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	org := orgtest.Organization(t, pool, "kinds", "Kinds", 3)
	_, err := pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
		VALUES ($1, 1, 'test.synthetic', '{"route": "here"}'), ($1, 2, 'test.unregistered', '{}'),
		($1, 3, 'message.posted', '{}')`, org)
	requireNoError(t, err)
	channel, topic := kernel.ID{15: 1}, kernel.ID{15: 2}
	var routed []byte
	kinds := realtime.Kinds{"test.synthetic": func(payload []byte) (kernel.ID, []kernel.ID, error) {
		routed = payload
		return channel, []kernel.ID{topic}, nil
	}}
	got, err := realtimepg.NewReader(pool, orgpg.BoundsIn, kinds).EventsAfter(t.Context(), org, 0, 10)
	requireNoError(t, err)
	want := []realtime.Event{
		{OrganizationID: org, Seq: 1, Kind: "test.synthetic", ChannelID: channel, Topics: []kernel.ID{topic}, Payload: routed},
		{OrganizationID: org, Seq: 2, Kind: "test.unregistered"},
		{OrganizationID: org, Seq: 3, Kind: conversation.KindPosted},
	}
	if len(routed) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("EventsAfter = %+v; want %+v", got, want)
	}
	kinds["test.synthetic"] = func([]byte) (kernel.ID, []kernel.ID, error) { return kernel.ID{}, nil, errors.New("malformed") }
	if events, err := realtimepg.NewReader(pool, orgpg.BoundsIn, kinds).EventsAfter(t.Context(), org, 0, 10); err == nil || len(events) != 0 {
		t.Fatalf("failed route: %+v, %v; want error without events", events, err)
	}
}

// An unknown organisation has an empty batch and no error, at any cursor and
// limit: there are no bounds to check against.
func TestEventsAfterUnknownOrganization(t *testing.T) {
	t.Parallel()
	reader := realtimepg.NewReader(pgtest.New(t), orgpg.BoundsIn, eventKinds(t))
	for _, limit := range []int{0, 10} {
		got, err := reader.EventsAfter(t.Context(), kernel.ID{0xee}, 5, limit)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("limit %d: %+v, %v; want an empty batch", limit, got, err)
		}
	}
}

// committingBounds commits once, after the reader's bounds and before its rows.
type committingBounds struct {
	realtime.Bounds
	commit *func()
}

func (b committingBounds) EventBounds(ctx context.Context, organizationID kernel.ID) (int64, int64, bool, error) {
	boundary, committed, found, err := b.Bounds.EventBounds(ctx, organizationID)
	if err == nil && *b.commit != nil {
		commit := *b.commit
		*b.commit = nil
		commit()
	}
	return boundary, committed, found, err
}

// The bounds and the rows come from one snapshot: a post and a cleanup that
// commit between the two reads change neither the batch nor its bounds.
func TestEventsAfterOneSnapshot(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "snapshot", "general")
	posting := newPosting(pool)
	first, err := posting.Post(ctx, membership(f), f.Channel.ID, "first")
	requireNoError(t, err)
	second, err := posting.Post(ctx, membership(f), f.Channel.ID, "second")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE event_log SET created_at = '2000-01-01' WHERE organization_id = $1 AND seq = $2", f.OrganizationID, first.EventSeq)
	requireNoError(t, err)
	var third conversation.Message
	commit := func() {
		var err error
		third, err = posting.Post(ctx, membership(f), f.Channel.ID, "third")
		requireNoError(t, err)
		requireNoError(t, realtimepg.NewCleaner(pool, orgpg.RetentionBoundaryIn).ExpireEvents(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
	}
	bounds := func(snapshot platform.Snapshot) realtime.Bounds {
		return committingBounds{Bounds: orgpg.BoundsIn(snapshot), commit: &commit}
	}
	cursor := first.EventSeq - 1
	got, err := realtimepg.NewReader(pool, bounds, eventKinds(t)).EventsAfter(ctx, f.OrganizationID, cursor, 10)
	if err != nil || len(got) != 2 || got[0].Seq != first.EventSeq || got[1].Seq != second.EventSeq {
		t.Fatalf("batch across a commit = %+v, %v; want the first two posts only", got, err)
	}
	// Both commits happened: a fresh read sees the cleanup and the new post.
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t))
	if _, err := reader.EventsAfter(ctx, f.OrganizationID, cursor, 10); !errors.Is(err, realtime.ErrCursorExpired) {
		t.Fatalf("cursor below the new boundary: %v; want ErrCursorExpired", err)
	}
	if got, err := reader.EventsAfter(ctx, f.OrganizationID, second.EventSeq, 10); err != nil || len(got) != 1 || got[0].Seq != third.EventSeq {
		t.Fatalf("after the commits = %+v, %v; want the third post", got, err)
	}
}
