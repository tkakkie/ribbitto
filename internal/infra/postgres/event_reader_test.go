package postgres_test

import (
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

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
	want := []domain.Event{
		{OrganizationID: f.OrganizationID, Seq: 1, Kind: domain.EventMemberJoined, MemberID: f.MemberID},
		{OrganizationID: f.OrganizationID, Seq: posted.EventSeq, Kind: domain.EventMessagePosted, ChannelID: f.Channel.ID, MessageID: posted.ID},
		{OrganizationID: f.OrganizationID, Seq: 3, Kind: "future.private", AudienceMemberID: &f.MemberID},
	}
	reader := postgres.NewEventReader(pool)
	for _, tt := range []struct {
		name  string
		after int64
		limit int
		want  []domain.Event
	}{
		{"ordered and scoped envelopes and IDs", 0, 10, want},
		{"limited", 0, 2, want[:2]},
		{"exclusive cursor", 1, 1, want[1:2]},
		{"next batch includes unknown kind", 2, 2, want[2:]},
		{"caught up", 3, 10, []domain.Event{}},
		{"zero limit", 0, 0, []domain.Event{}},
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
