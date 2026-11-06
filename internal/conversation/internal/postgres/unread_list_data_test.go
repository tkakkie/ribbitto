package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

type unreadListChannelShape struct {
	topics, messages, unread, unreadTopics, beyond50, ranges, floors int64
}

type unreadListDataShape struct {
	channels int
	ordinary unreadListChannelShape
	first    unreadListChannelShape
}

func unreadListBenchExpected(name string) unreadListDataShape {
	want := unreadListDataShape{channels: 10, ordinary: unreadListChannelShape{50, 50, 5, 5, 0, 4, 50}}
	switch name {
	case "channels-100", "largest":
		want.channels, want.ordinary = 100, unreadListChannelShape{60, 60, 6, 6, 1, 5, 60}
	case "caught-up":
		want.ordinary = unreadListChannelShape{50, 50, 0, 0, 0, 0, 0}
	}
	want.first = want.ordinary
	if name == "largest" {
		want.channels = 500
		want.first = unreadListChannelShape{60, 110_000, 100_000, 60, 10, 10_000, 60}
	}
	return want
}

// This gate deliberately shares no candidate SQL, read-state loader or oracle.
// Count actual messages with a plain anti-join, without any display/count cap.
func unreadListVerifyData(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, f unreadListFixture, want unreadListDataShape) error {
	rows, err := db.Query(ctx, `WITH topics AS (
 SELECT t.*, row_number() OVER (PARTITION BY channel_id ORDER BY is_default DESC,
  (conclusion_message_id IS NOT NULL),lower(name),id) AS position
 FROM topic t WHERE organization_id=$1
), messages AS (
 SELECT m.*, m.event_seq >= greatest($3::bigint+1,coalesce((SELECT hi FROM read_range
   WHERE organization_id=$1 AND channel_id=m.channel_id AND member_id=$2 AND lo=0),0))
  AND (m.event_seq>coalesce(f.floor_seq,0) OR m.moved_event_seq>coalesce(f.floor_seq,0))
  AND NOT EXISTS (SELECT 1 FROM read_range r WHERE r.organization_id=$1
   AND r.channel_id=m.channel_id AND r.member_id=$2 AND r.lo<=m.event_seq AND m.event_seq<r.hi) AS unread
 FROM message m LEFT JOIN topic_read_floor f ON f.organization_id=m.organization_id
  AND f.channel_id=m.channel_id AND f.topic_id=m.topic_id AND f.member_id=$2
 WHERE m.organization_id=$1
), ranges AS (
 SELECT *,max(hi) OVER (PARTITION BY channel_id ORDER BY lo
  ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS previous_hi
 FROM read_range WHERE organization_id=$1 AND member_id=$2
)
SELECT c.id,count(DISTINCT t.id),count(m.id),count(m.id) FILTER (WHERE m.unread),
 count(DISTINCT t.id) FILTER (WHERE m.unread),
 count(DISTINCT t.id) FILTER (WHERE m.unread AND t.position>50),
 (SELECT count(*) FROM ranges r WHERE r.channel_id=c.id AND r.lo>0),
 (SELECT count(*) FROM topic_read_floor f WHERE f.organization_id=$1 AND f.member_id=$2 AND f.channel_id=c.id),
 (SELECT count(*) FROM ranges r WHERE r.channel_id=c.id AND r.lo=0),
 (SELECT count(*) FROM ranges r WHERE r.channel_id=c.id AND r.lo<=r.previous_hi)
FROM channel c LEFT JOIN topics t ON t.channel_id=c.id
 LEFT JOIN messages m ON m.channel_id=c.id AND m.topic_id=t.id
WHERE c.organization_id=$1 GROUP BY c.id`, f.member.Organization.ID, f.member.Member.ID, f.member.Member.JoinedEventSeq)
	if err != nil {
		return fmt.Errorf("independent data sanity: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var channel kernel.ID
		var got unreadListChannelShape
		var prefixes, invalid int64
		if err := rows.Scan(&channel, &got.topics, &got.messages, &got.unread, &got.unreadTopics, &got.beyond50, &got.ranges, &got.floors, &prefixes, &invalid); err != nil {
			return fmt.Errorf("reading independent data sanity: %w", err)
		}
		expected := want.ordinary
		if len(f.channels) > 0 && channel == f.channels[0] {
			expected = want.first
		}
		if !slices.Contains(f.channels, channel) || got != expected || prefixes != 1 || invalid != 0 {
			return fmt.Errorf("independent data sanity channel=%s: got=%+v want=%+v prefixes=%d want=1 overlapping/adjacent=%d want=0", channel, got, expected, prefixes, invalid)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading independent data sanity: %w", err)
	}
	if seen != want.channels || len(f.channels) != want.channels {
		return fmt.Errorf("independent data sanity channels=%d supplied=%d want=%d", seen, len(f.channels), want.channels)
	}
	return nil
}

func TestUnreadListDataSanity(t *testing.T) {
	pool := pgtest.New(t)
	base := conversationtest.OrganizationWithOwner(t, pool, "data-sanity", "general")
	writer := orgtest.Member(t, pool, base.OrganizationID, identitytest.Account(t, pool, "sanity@example.org", "Writer"), org.RoleMember, "writer", 1)
	f := unreadListFixture{pool: pool, member: org.Membership{Organization: org.Organization{ID: base.OrganizationID}, Member: org.Member{ID: base.MemberID, JoinedEventSeq: 1}}, channels: []kernel.ID{base.Channel.ID}}
	// Hand-built, not even a tiny invocation of the Stage 1 seeder: two read
	// singletons, one prefix, an inclusive floor, a moved-in hole and a topic
	// beyond the sidebar. Empty topics make the boundary cheap to exercise.
	topics := []kernel.ID{base.Channel.DefaultTopicID}
	for i := 1; i <= 51; i++ {
		topics = append(topics, conversationtest.Topic(t, pool, base.OrganizationID, base.Channel.ID, fmt.Sprintf("topic-%02d", i)).ID)
	}
	exec := func(sql string, args ...any) { unreadListExec(t.Context(), t, pool, sql, args...) }
	exec(`INSERT INTO channel_read VALUES ($1,$2,$3)`, base.OrganizationID, base.Channel.ID, base.MemberID)
	for _, r := range [][2]int64{{0, 3}, {5, 6}, {8, 9}} {
		exec(`INSERT INTO read_range VALUES ($1,$2,$3,$4,$5)`, base.OrganizationID, base.Channel.ID, base.MemberID, r[0], r[1])
	}
	exec(`INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,4)`, base.OrganizationID, topics[0], base.MemberID, base.Channel.ID)
	for _, m := range []struct {
		seq   int64
		topic int
		moved any
	}{{1, 0, nil}, {2, 0, nil}, {3, 0, int64(10)}, {4, 0, nil}, {5, 0, nil}, {6, 0, nil}, {8, 0, nil}, {9, 51, nil}, {7, 0, nil}} {
		exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq) VALUES ($1,$2,$3,$4,'hand-built',$5,$6)`, base.OrganizationID, base.Channel.ID, topics[m.topic], writer, m.seq, m.moved)
	}
	want := unreadListDataShape{channels: 1, first: unreadListChannelShape{52, 9, 4, 2, 1, 2, 1}}
	for _, tt := range []struct{ name, sql string }{
		{"intact", ""},
		{"delete-message", `DELETE FROM message WHERE event_seq=9`},
		{"delete-empty-topic", `DELETE FROM topic WHERE name='topic-01'`},
		{"delete-range", `DELETE FROM read_range WHERE lo=5`},
		{"merge-ranges", `DELETE FROM read_range WHERE lo=8; UPDATE read_range SET hi=9 WHERE lo=5`},
		{"adjacent-ranges", `UPDATE read_range SET hi=8 WHERE lo=5`},
		{"overlapping-ranges", `UPDATE read_range SET hi=10 WHERE lo=5`},
		{"delete-floor", `DELETE FROM topic_read_floor`},
		{"floor-hides-unread", `UPDATE topic_read_floor SET floor_seq=10`},
		{"move-at-floor", `UPDATE message SET moved_event_seq=4 WHERE event_seq=3`},
		{"prefix-hides-unread", `UPDATE read_range SET hi=7 WHERE lo=0`},
		{"wrong-channel-count", ""},
		{"wrong-join-prefix", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			requireNoError(t, err)
			defer func() { requireNoError(t, tx.Rollback(t.Context())) }()
			if tt.sql != "" {
				_, err = tx.Exec(t.Context(), tt.sql)
				requireNoError(t, err)
			}
			expected, fixture := want, f
			if tt.name == "wrong-channel-count" {
				expected.channels++
			}
			if tt.name == "wrong-join-prefix" {
				fixture.member.Member.JoinedEventSeq = 6
			}
			err = unreadListVerifyData(t.Context(), tx, fixture, expected)
			if (err != nil) != (tt.name != "intact") {
				t.Fatalf("data sanity error=%v mutation=%s", err, tt.name)
			}
		})
	}
	// A second hand-built member proves the independent zero-unread check.
	caught := orgtest.Member(t, pool, base.OrganizationID, identitytest.Account(t, pool, "caught@example.org", "Caught"), org.RoleMember, "caught", 1)
	exec(`INSERT INTO channel_read VALUES ($1,$2,$3)`, base.OrganizationID, base.Channel.ID, caught)
	exec(`INSERT INTO read_range VALUES ($1,$2,$3,0,100)`, base.OrganizationID, base.Channel.ID, caught)
	f.member.Member.ID = caught
	want.first = unreadListChannelShape{52, 9, 0, 0, 0, 0, 0}
	requireNoError(t, unreadListVerifyData(t.Context(), pool, f, want))
}
