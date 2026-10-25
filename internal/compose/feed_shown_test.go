package compose_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func shownFixture(t *testing.T) (*pgxpool.Pool, unread.Scope, kernel.ID) {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "shown", "general")
	_, err := pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'shown',n FROM generate_series(2,42) n", f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	feedRequire(t, err)
	_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq=42,event_log_boundary_seq=42 WHERE id=$1", f.OrganizationID)
	feedRequire(t, err)
	reader := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "reader@example.org", "Reader"), org.RoleMember, "reader", 2)
	return pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: reader}, f.Channel.DefaultTopicID
}

func checkShown(t *testing.T, pool *pgxpool.Pool, s unread.Scope, topic kernel.ID, joined, from, through int64) {
	t.Helper()
	state := readOracleLoad(t, pool, s)
	model := make(map[int64]bool)
	for _, m := range state.messages {
		model[m.seq] = m.seq <= joined || (from <= m.seq && m.seq <= through)
	}
	for _, v := range readOracleCheckState(state, joined, model) {
		t.Error(v)
	}
	member := org.Membership{Organization: org.Organization{ID: s.OrganizationID}, Member: org.Member{ID: s.MemberID, JoinedEventSeq: joined}}
	page, err := conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn).Page(t.Context(), member, s.ChannelID, nil, nil)
	feedRequire(t, err)
	for _, v := range readOracleCheckCounts(page.PageCounts, map[kernel.ID]readOracleState{s.ChannelID: state}, s.ChannelID, []kernel.ID{topic}, nil, joined) {
		t.Error(v)
	}
}

func TestFeedReadShownRanges(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		joined, from, through, cursor int64
		ranges                        []conversation.SequenceRange
	}{
		{"exact boundaries", 2, 10, 20, 41, []conversation.SequenceRange{{Lo: 0, Hi: 3}, {Lo: 10, Hi: 21}}},
		{"no predecessor", 2, 1, 3, 41, []conversation.SequenceRange{{Lo: 0, Hi: 4}}},
		{"no successor", 2, 40, 42, 42, []conversation.SequenceRange{{Lo: 0, Hi: 3}, {Lo: 40, Hi: 43}}},
		{"join prefix only boundary", 2, 7, 8, 41, []conversation.SequenceRange{{Lo: 0, Hi: 9}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, s, topic := shownFixture(t)
			if tc.name == "join prefix only boundary" {
				// The first write must preserve seq == joined through the join prefix alone;
				// lo=F leaves an empty gap, forbidden by #1486 (decision 32, amended).
				_, err := pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1 AND channel_id=$2 AND 2 < event_seq AND event_seq < 8", s.OrganizationID, s.ChannelID)
				feedRequire(t, err)
			}
			reading := unreadpg.NewReading(pool, compose.NewFeedWriter(), nil, nil)
			refuse := func() {
				for _, b := range [][3]int64{{41, 20, 10}, {41, 1, 42}, {41, 0, 10}, {43, 1, 10}, {math.MaxInt64, 1, 10}, {-1, 1, 1}} {
					before := readOracleRows(t, pool, nil, false)
					if err := reading.FeedShown(t.Context(), s, tc.joined, b[0], b[1], b[2]); !errors.Is(err, unread.ErrInvalidCursor) {
						t.Fatalf("bounds=%v: %v", b, err)
					}
					for _, v := range readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "refusal") {
						t.Error(v)
					}
				}
			}
			// Fresh refusals must not even create the channel lock or prefix.
			refuse()
			feedRequire(t, reading.FeedShown(t.Context(), s, tc.joined, tc.cursor, tc.from, tc.through))
			checkShown(t, pool, s, topic, tc.joined, tc.from, tc.through)
			feedRanges(t, pool, s, tc.ranges)
			_, err := pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, s.ChannelID, s.MemberID, topic, tc.joined)
			feedRequire(t, err)
			refuse()
			before := readOracleRows(t, pool, nil, false)
			feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				return checkedRead(t, false)(compose.NewFeedWriter().ReadShown(t.Context(), tx, s, tc.joined, tc.cursor, tc.from, tc.through))
			}))
			for _, v := range readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "repeat") {
				t.Error(v)
			}
		})
	}
}

func TestFeedReadShownNotMessageAligned(t *testing.T) {
	pool, s, topic := shownFixture(t)
	other := conversationtest.Channel(t, pool, s.OrganizationID, "other", false)
	_, err := pool.Exec(t.Context(), "UPDATE message SET channel_id=$2,topic_id=$3 WHERE organization_id=$1 AND event_seq IN (9,21)", s.OrganizationID, other.ID, other.DefaultTopicID)
	feedRequire(t, err)
	// Sequence 22 stands for an event without a message; 9 and 21 name another channel.
	_, err = pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1 AND event_seq=22", s.OrganizationID)
	feedRequire(t, err)
	_, err = pool.Exec(t.Context(), "INSERT INTO event_log (organization_id,seq,kind,data) VALUES ($1,22,'topic.created','{}')", s.OrganizationID)
	feedRequire(t, err)
	feedRequire(t, unreadpg.NewReading(pool, compose.NewFeedWriter(), nil, nil).FeedShown(t.Context(), s, 2, 41, 9, 22))
	feedRanges(t, pool, s, []conversation.SequenceRange{{Lo: 0, Hi: 3}, {Lo: 9, Hi: 23}})
	checkShown(t, pool, s, topic, 2, 9, 22)
}

func TestShownReadStatements(t *testing.T) {
	t.Run("topic", testTopicShownStatements)
	for _, tc := range []struct {
		name                         string
		joined, first, cursor, floor int64
		stored                       bool
	}{
		{"empty", 1, 0, 5, -1, false}, {"before first message", 1, 0, 5, -1, false},
		{"join prefix", 10, 0, 10, 0, false}, {"join prefix fresh", 10, 0, 10, -1, false},
		{"repeated", 1, 10, 10, -1, false}, {"older", 1, 20, 5, -1, false},
		{"event only extension", 1, 10, 20, -1, false}, {"identical range", 1, 0, 10, 0, true},
		{"fragmented no match", 1, 0, 20000, 0, false}, {"fragmented match", 1, 0, 20000, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, s, topic := shownFixture(t)
			_, err := pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1", s.OrganizationID)
			feedRequire(t, err)
			if tc.name != "empty" && !strings.HasPrefix(tc.name, "fragmented") {
				_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'read',10)", s.OrganizationID, s.ChannelID, topic, s.MemberID)
				feedRequire(t, err)
			}
			if tc.floor >= 0 {
				_, err = pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, s.ChannelID, s.MemberID, topic, tc.floor)
				feedRequire(t, err)
			}
			if tc.stored {
				_, err = pool.Exec(t.Context(), "INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) VALUES ($1,$2,$3,10,11)", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
			}
			if strings.HasPrefix(tc.name, "fragmented") {
				_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq=20000,event_log_boundary_seq=20000 WHERE id=$1", s.OrganizationID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) SELECT $1,$2,$3,n,n+1 FROM generate_series(2,20000,2) n", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'read',n FROM generate_series(2,20000,2) n", s.OrganizationID, s.ChannelID, topic, s.MemberID)
				feedRequire(t, err)
				if tc.name == "fragmented match" {
					_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'unread',19999)", s.OrganizationID, s.ChannelID, topic, s.MemberID)
					feedRequire(t, err)
				}
			}
			counter := platform.NewQueryCounter()
			config := pool.Config()
			config.ConnConfig.Tracer = counter
			traced, err := pgxpool.NewWithConfig(t.Context(), config)
			feedRequire(t, err)
			defer traced.Close()
			rollback := errors.New("count without persisting")
			for _, cursor := range []int64{tc.first, tc.cursor} {
				if cursor == 0 {
					continue
				}
				var counts [2]int64
				for i := range counts {
					err = platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
						before := counter.Counts().Queries
						var readErr error
						if i == 0 {
							_, readErr = compose.NewFeedWriter().Read(t.Context(), tx, s, tc.joined, cursor)
						} else {
							_, readErr = compose.NewFeedWriter().ReadShown(t.Context(), tx, s, tc.joined, cursor, 1, cursor)
						}
						feedRequire(t, readErr)
						counts[i] = counter.Counts().Queries - before
						return rollback
					})
					if !errors.Is(err, rollback) {
						t.Fatal(err)
					}
				}
				if counts[1] != counts[0]+1 {
					t.Fatalf("cursor=%d statements=%v, want today + 1", cursor, counts)
				}
				if cursor == tc.first {
					feedRequire(t, unreadpg.NewReading(pool, compose.NewFeedWriter(), nil, nil).FeedShown(t.Context(), s, tc.joined, cursor, 1, cursor))
				}
			}
		})
	}
}
