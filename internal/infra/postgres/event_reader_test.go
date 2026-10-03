package postgres_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestPostedEventTopic(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "posted-topic", "general")
	named, err := postgres.NewTopicStore(pool).CreateTopic(ctx, f.OrganizationID, f.Channel.ID, "design")
	requireNoError(t, err)
	posted, err := postgres.NewPostingStore(pool).PostToTopic(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, &named.ID, "hello")
	requireNoError(t, err)
	reader := postgres.NewEventReader(pool)
	events, err := reader.EventsAfter(ctx, f.OrganizationID, posted.EventSeq-1, 1)
	requireNoError(t, err)
	if len(events) != 1 || events[0].TopicID == nil || *events[0].TopicID != named.ID {
		t.Fatalf("named-topic post: %+v", events)
	}
	// A missing field is readable legacy data. Every present invalid value
	// must fail the whole batch, including the valid event preceding it.
	_, err = postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "second")
	requireNoError(t, err)
	for _, value := range []string{"", `null`, `false`, `42`, `[]`, `{}`, `""`, `"bad"`, `"00000000x0000x0000x0000x000000000001"`} {
		t.Run("topic="+value, func(t *testing.T) {
			_, err := pool.Exec(ctx, "UPDATE event_log SET data = data - 'topic_id' WHERE organization_id = $1 AND seq = $2", f.OrganizationID, posted.EventSeq+1)
			requireNoError(t, err)
			if value != "" {
				_, err = pool.Exec(ctx, "UPDATE event_log SET data = jsonb_set(data, '{topic_id}', $3::jsonb) WHERE organization_id = $1 AND seq = $2", f.OrganizationID, posted.EventSeq+1, json.RawMessage(value))
				requireNoError(t, err)
			}
			events, err := reader.EventsAfter(ctx, f.OrganizationID, posted.EventSeq-1, 2)
			if value == "" {
				requireNoError(t, err)
				if len(events) != 2 || events[1].TopicID != nil {
					t.Fatalf("legacy post: %+v", events)
				}
			} else if err == nil || len(events) != 0 {
				t.Fatalf("malformed topic: %+v, %v; want error without events", events, err)
			}
		})
	}
}

func TestEventsAfter(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "events", "general")
	other := pgtest.OrganizationWithOwner(t, pool, "other", "general")
	// Insert out of sequence, including a future payload the reader cannot decode.
	_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 3, 'future.private', $2, '{"channel_id":"future-format"}')`, f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	posted, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "hello")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data)
		VALUES ($1, 1, 'member.joined', jsonb_build_object('member_id', $2::uuid))`, f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	_, err = postgres.NewPostingStore(pool).Post(ctx, other.OrganizationID, other.Channel.ID, other.MemberID, "other org")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_seq = 3 WHERE id = $1", f.OrganizationID)
	requireNoError(t, err)
	want := []realtime.Event{
		{OrganizationID: f.OrganizationID, Seq: 1, Kind: realtime.EventMemberJoined, MemberID: f.MemberID},
		{OrganizationID: f.OrganizationID, Seq: posted.EventSeq, Kind: realtime.EventMessagePosted, ChannelID: f.Channel.ID, MessageID: posted.ID, TopicID: &posted.TopicID},
		{OrganizationID: f.OrganizationID, Seq: 3, Kind: "future.private", AudienceMemberID: &f.MemberID},
	}
	reader := postgres.NewEventReader(pool)
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
	// A known event can also target a single member; audience is not payload data.
	_, err = pool.Exec(ctx, "UPDATE event_log SET audience_member_id = $2 WHERE organization_id = $1 AND seq = 2", f.OrganizationID, f.MemberID)
	requireNoError(t, err)
	want[1].AudienceMemberID = &f.MemberID
	got, err := reader.EventsAfter(ctx, f.OrganizationID, 1, 1)
	requireNoError(t, err)
	if !reflect.DeepEqual(got, want[1:2]) {
		t.Fatalf("targeted event: %+v; want %+v", got, want[1:2])
	}
}

func TestEventsAfterMalformedData(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	org := pgtest.Organization(t, pool, "malformed", "Malformed", 1)
	reader := postgres.NewEventReader(pool)
	for _, tt := range []struct{ kind, data string }{
		{"message.posted", `{"channel_id":"invalid","message_id":"invalid"}`},
		{"message.posted", `{"channel_id":"00000000-0000-0000-0000-000000000001","message_id":false}`},
		{"message.posted", `{"channel_id":"00000000-0000-0000-0000-000000000001"}`},
		{"message.posted", `{"channel_id":null,"message_id":null}`},
		{"member.joined", `{"member_id":"invalid"}`},
		{"member.joined", `{"member_id":11111111111111111111111111111111111111}`},
		{"member.joined", `{"member_id":"00000000x0000x0000x0000x000000000001"}`},
		{"member.joined", `{}`},
		{"member.joined", `null`},
		{"member.joined", `[]`},
	} {
		t.Run(tt.kind+"/"+tt.data, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), `INSERT INTO event_log (organization_id, seq, kind, data)
				VALUES ($1, 1, $2, $3) ON CONFLICT (organization_id, seq) DO UPDATE SET kind = EXCLUDED.kind, data = EXCLUDED.data`, org, tt.kind, tt.data)
			requireNoError(t, err)
			if events, err := reader.EventsAfter(t.Context(), org, 0, 10); err == nil || len(events) != 0 {
				t.Fatalf("malformed event: %+v, %v; want error without events", events, err)
			}
		})
	}
}

func TestEventsAfterCursorAboveLog(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "cursor", "general")
	posted, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "hello")
	requireNoError(t, err)
	empty := pgtest.Organization(t, pool, "empty", "Empty", 0)
	reader := postgres.NewEventReader(pool)
	for _, tt := range []struct {
		name string
		org  domain.ID
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

func TestCommittedSequences(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "watermark", "general")
	other := pgtest.OrganizationWithOwner(t, pool, "watermark-other", "general")
	posted, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "hello")
	requireNoError(t, err)
	reader := postgres.NewEventReader(pool)
	unknown := domain.ID{0xee}
	got, err := reader.CommittedSequences(ctx, []domain.ID{f.OrganizationID, other.OrganizationID, unknown})
	requireNoError(t, err)
	var otherSeq int64
	requireNoError(t, pool.QueryRow(ctx, "SELECT event_seq FROM organization WHERE id = $1", other.OrganizationID).Scan(&otherSeq))
	want := map[domain.ID]int64{f.OrganizationID: posted.EventSeq, other.OrganizationID: otherSeq}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CommittedSequences = %v, want %v (an unknown organisation is left out)", got, want)
	}
	got, err = reader.CommittedSequences(ctx, []domain.ID{f.OrganizationID})
	requireNoError(t, err)
	if want := map[domain.ID]int64{f.OrganizationID: posted.EventSeq}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommittedSequences(f) = %v, want %v (an existing organisation not asked for is left out)", got, want)
	}
	got, err = reader.CommittedSequences(ctx, nil)
	requireNoError(t, err)
	if len(got) != 0 {
		t.Fatalf("CommittedSequences(nil) = %v, want empty", got)
	}
}

func TestEventRetentionTransaction(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "retention", "general")
	for range 2 {
		_, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "kept")
		requireNoError(t, err)
	}
	_, err := pool.Exec(ctx, "UPDATE event_log SET created_at = CASE WHEN seq = 2 THEN '2000-01-01'::timestamptz ELSE '2100-01-01'::timestamptz END WHERE organization_id = $1", f.OrganizationID)
	requireNoError(t, err)
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	requireNoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	id := pgtype.UUID{Bytes: f.OrganizationID, Valid: true}
	requireNoError(t, q.LockEventRetentionOrganization(ctx, id))
	count, err := q.ExpireEventBatch(ctx, sqlcgen.ExpireEventBatchParams{OrganizationID: id, Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}})
	requireNoError(t, err)
	if count != 1 {
		t.Fatalf("deleted %d rows, want 1", count)
	}
	// Until commit, a reader sees both the old boundary and every old row.
	reader := postgres.NewEventReader(pool)
	rows, err := reader.EventsAfter(ctx, f.OrganizationID, 1, 10)
	requireNoError(t, err)
	if len(rows) != 2 {
		t.Fatalf("uncommitted cleanup hid rows: %v", rows)
	}
	requireNoError(t, tx.Commit(ctx))
	var remaining int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM event_log WHERE organization_id = $1 AND seq <= 2", f.OrganizationID).Scan(&remaining))
	if remaining != 0 {
		t.Fatalf("expired rows remain: %d", remaining)
	}
	rows, err = reader.EventsAfter(ctx, f.OrganizationID, 2, 10)
	requireNoError(t, err)
	if len(rows) != 1 || rows[0].Seq != 3 {
		t.Fatalf("boundary cursor lost recent event: %v", rows)
	}
	if _, err := reader.EventsAfter(ctx, f.OrganizationID, 1, 10); !errors.Is(err, realtime.ErrCursorExpired) {
		t.Fatalf("below boundary: %v", err)
	}
}
