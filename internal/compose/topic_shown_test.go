package compose_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func checkTopicShown(t *testing.T, pool *pgxpool.Pool, s unread.TopicScope, model map[int64]bool) conversation.PageCounts {
	t.Helper()
	state := readOracleLoad(t, pool, s.Scope)
	for _, v := range readOracleCheckState(state, 2, model) {
		t.Error(v)
	}
	member := org.Membership{Organization: org.Organization{ID: s.OrganizationID}, Member: org.Member{ID: s.MemberID, JoinedEventSeq: 2}}
	page, err := conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn).Page(t.Context(), member, s.ChannelID, &s.TopicID, nil)
	feedRequire(t, err)
	for _, v := range readOracleCheckCounts(page.PageCounts, map[kernel.ID]readOracleState{s.ChannelID: state}, s.ChannelID, []kernel.ID{s.TopicID}, &s.TopicID, 2) {
		t.Error(v)
	}
	return page.PageCounts
}

func TestTopicReadShownFloorStore(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		seed, from, through, floor int64
	}{
		{"no blocker F unread", 9, 10, 20, 20}, {"partial raise", 0, 10, 20, 4},
		{"F minus one blocker", 0, 10, 20, 8}, {"T below S boundaries", 9, 10, 20, 20},
		{"first write join boundary", 0, 10, 20, 2}, {"non message bounds", 0, 9, 22, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope, topic := shownFixture(t)
			s := unread.TopicScope{Scope: scope, TopicID: topic}
			reading := unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
			refuse := func() {
				for _, b := range []struct {
					name               string
					s, f, t, committed int64
				}{
					{"F below one", 41, 0, 20, 42}, {"T below F", 41, 20, 10, 42}, {"T above S", 19, 10, 20, 42},
					{"S above committed", 43, 10, 20, 42}, {"S MaxInt64 committed", math.MaxInt64, 10, 20, math.MaxInt64}, {"S negative", -1, 1, 1, 42},
				} {
					t.Run("refusal/"+b.name, func(t *testing.T) {
						_, err := pool.Exec(t.Context(), "UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1", s.OrganizationID, b.committed)
						feedRequire(t, err)
						before := readOracleRows(t, pool, nil, false)
						if err := reading.TopicShown(t.Context(), s, 2, b.s, b.f, b.t); !errors.Is(err, unread.ErrInvalidCursor) {
							t.Fatalf("refusal: %v", err)
						}
						for _, v := range readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "refusal") {
							t.Error(v)
						}
					})
				}
				_, err := pool.Exec(t.Context(), "UPDATE organization SET event_seq=42,event_log_boundary_seq=42 WHERE id=$1", s.OrganizationID)
				feedRequire(t, err)
			}
			if tc.name == "no blocker F unread" {
				refuse()
			}
			if tc.name == "partial raise" || tc.name == "F minus one blocker" {
				other := conversationtest.Topic(t, pool, s.OrganizationID, s.ChannelID, "unread below blocker")
				_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND 2 < event_seq AND event_seq <= $3", s.OrganizationID, other.ID, tc.floor)
				feedRequire(t, err)
			}
			if tc.seed > 0 {
				feedRequire(t, reading.FeedShown(t.Context(), scope, 2, 41, 1, tc.seed))
			}
			if tc.name == "non message bounds" {
				_, err := pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1 AND event_seq IN (9,22)", s.OrganizationID)
				feedRequire(t, err)
			}
			feedRequire(t, reading.TopicShown(t.Context(), s, 2, 41, tc.from, tc.through))
			topicFloor(t, pool, s, tc.floor)
			model := make(map[int64]bool)
			for _, m := range readOracleLoad(t, pool, scope).messages {
				model[m.seq] = m.seq <= max(2, tc.seed) || (tc.from <= m.seq && m.seq <= tc.through)
			}
			checkTopicShown(t, pool, s, model)
			if tc.name == "no blocker F unread" {
				refuse()
			}
			before := readOracleRows(t, pool, nil, false)
			feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				return checkedRead(t, false)(compose.NewTopicWriter().ReadShown(t.Context(), tx, s, 2, 41, tc.from, tc.through))
			}))
			for _, v := range readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "repeat") {
				t.Error(v)
			}
		})
	}
}

func TestShownReadMoves(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		seq, moved, initial, floor int64
		out                        bool
	}{
		{"after cursor inside run", 25, 42, 0, 30, false}, {"before cursor inside run", 25, 40, 0, 30, false},
		{"below run above floor", 10, 40, 0, 9, false}, {"below floor exception", 10, 20, 15, 19, false},
		{"moved out", 25, 40, 0, 30, true}, {"moved at F", 10, 20, 15, 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope, topic := shownFixture(t)
			s := unread.TopicScope{Scope: scope, TopicID: topic}
			other := conversationtest.Topic(t, pool, s.OrganizationID, s.ChannelID, "other")
			_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND event_seq NOT IN (20,30)", s.OrganizationID, other.ID)
			feedRequire(t, err)
			reading := unreadpg.NewReading(pool, compose.NewFeedWriter(), compose.NewTopicWriter(), nil)
			if tc.initial > 0 {
				feedRequire(t, reading.Topic(t.Context(), s, 2, tc.initial))
			}
			target := topic
			if tc.out {
				target = other.ID
			}
			_, err = pool.Exec(t.Context(), "UPDATE message SET topic_id=$2,moved_event_seq=$3 WHERE organization_id=$1 AND event_seq=$4", s.OrganizationID, target, tc.moved, tc.seq)
			feedRequire(t, err)
			from := int64(20)
			if tc.name == "moved at F" {
				from = 10
			}
			feedRequire(t, reading.TopicShown(t.Context(), s, 2, 41, from, 30))
			model := make(map[int64]bool)
			for _, m := range readOracleLoad(t, pool, scope).messages {
				model[m.seq] = m.seq <= 2 || (m.topic == topic && from <= m.seq && m.seq <= 30 && m.moved <= 41)
			}
			checkTopicShown(t, pool, s, model)
			topicFloor(t, pool, s, tc.floor)
			if tc.name == "below floor exception" {
				// A later run must still count the unread move under the stored floor.
				feedRequire(t, reading.TopicShown(t.Context(), s, 2, 41, 35, 38))
				topicFloor(t, pool, s, 19)
				checkTopicShown(t, pool, s, model)
			}
		})
	}
}

func TestShownReadConcurrentAndReordered(t *testing.T) {
	for _, kinds := range [][2]string{{"topic", "topic"}, {"topic", "feed"}, {"feed", "topic"}} {
		for _, runs := range [][2][2]int64{{{1, 10}, {11, 20}}, {{5, 15}, {10, 20}}} {
			t.Run(fmt.Sprint(kinds, runs), func(t *testing.T) {
				var wantRanges []conversation.SequenceRange
				var wantCounts conversation.PageCounts
				for _, order := range []string{"forward", "reverse", "concurrent"} {
					t.Run(order, func(t *testing.T) {
						pool, scope, topic := shownFixture(t)
						s := unread.TopicScope{Scope: scope, TopicID: topic}
						ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
						defer cancel()
						locked, release := make(chan struct{}), make(chan struct{})
						write := func(i int, hold bool) error {
							return platform.InTx(ctx, pool, func(tx platform.Tx) error {
								var err error
								if kinds[i] == "feed" {
									_, err = compose.NewFeedWriter().ReadShown(ctx, tx, scope, 2, 41, runs[i][0], runs[i][1])
								} else {
									_, err = compose.NewTopicWriter().ReadShown(ctx, tx, s, 2, 41, runs[i][0], runs[i][1])
								}
								if err != nil {
									return err
								}
								if hold {
									close(locked)
									select {
									case <-release:
									case <-ctx.Done():
										return ctx.Err()
									}
								}
								return nil
							})
						}
						if order == "concurrent" {
							first, second := make(chan error, 1), make(chan error, 1)
							go func() { first <- write(0, true) }()
							select {
							case <-locked:
							case err := <-first:
								t.Fatalf("first writer: %v", err)
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							}
							go func() { second <- write(1, false) }()
							for {
								var blocked bool
								feedRequire(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid)) > 0)").Scan(&blocked))
								if blocked {
									break
								}
								select {
								case err := <-second:
									t.Fatalf("second writer did not wait: %v", err)
								default:
								}
							}
							close(release)
							feedRequire(t, <-first)
							feedRequire(t, <-second)
						} else {
							first := 0
							if order == "reverse" {
								first = 1
							}
							feedRequire(t, write(first, false))
							feedRequire(t, write(1-first, false))
						}
						state := readOracleLoad(t, pool, scope)
						model := make(map[int64]bool)
						for _, m := range state.messages {
							model[m.seq] = m.seq <= 2 || (runs[0][0] <= m.seq && m.seq <= runs[0][1]) || (runs[1][0] <= m.seq && m.seq <= runs[1][1])
						}
						counts := checkTopicShown(t, pool, s, model)
						// IDs differ between fixtures; compare only values, never floors across orders.
						counts.ChannelCounts = map[kernel.ID]int64{{}: counts.ChannelCounts[scope.ChannelID]}
						counts.TopicCounts = map[kernel.ID]int64{{}: counts.TopicCounts[topic]}
						if order == "forward" {
							wantRanges, wantCounts = state.ranges, counts
						} else if !slices.Equal(state.ranges, wantRanges) || !reflect.DeepEqual(counts, wantCounts) {
							t.Fatalf("ranges/counts/first unread differ: %v %+v", state.ranges, counts)
						}
					})
				}
			})
		}
	}
}

func testTopicShownStatements(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		joined, first, cursor, floor int64
		stored                       bool
	}{
		{"empty", 1, 0, 5, -1, false}, {"before first message", 1, 0, 5, -1, false},
		{"join prefix", 10, 0, 10, 0, false}, {"join prefix fresh", 10, 0, 10, -1, false},
		{"repeated", 1, 10, 10, -1, false}, {"older", 1, 20, 5, -1, false},
		{"event only extension", 1, 10, 20, -1, false}, {"identical range", 1, 0, 10, 0, true}, {"floor only", 1, 0, 20, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope, topic := shownFixture(t)
			s := unread.TopicScope{Scope: scope, TopicID: topic}
			_, err := pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1 AND event_seq<>10", s.OrganizationID)
			feedRequire(t, err)
			if tc.name == "empty" {
				_, err = pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1", s.OrganizationID)
				feedRequire(t, err)
			}
			if tc.floor >= 0 {
				_, err = pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, s.ChannelID, s.MemberID, s.TopicID, tc.floor)
				feedRequire(t, err)
			}
			if tc.stored {
				_, err = pool.Exec(t.Context(), "INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) VALUES ($1,$2,$3,10,11)", s.OrganizationID, s.ChannelID, s.MemberID)
				feedRequire(t, err)
			}
			counter := platform.NewQueryCounter()
			config := pool.Config()
			config.ConnConfig.Tracer = counter
			traced, err := pgxpool.NewWithConfig(t.Context(), config)
			feedRequire(t, err)
			defer traced.Close()
			rollback := errors.New("count only")
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
							_, readErr = compose.NewTopicWriter().Read(t.Context(), tx, s, tc.joined, cursor)
						} else {
							_, readErr = compose.NewTopicWriter().ReadShown(t.Context(), tx, s, tc.joined, cursor, 1, cursor)
						}
						if readErr != nil {
							return readErr
						}
						counts[i] = counter.Counts().Queries - before
						return rollback
					})
					if !errors.Is(err, rollback) {
						t.Fatal(err)
					}
				}
				if counts[1] != counts[0]+1 {
					t.Fatalf("cursor=%d statements=%v want today + 1", cursor, counts)
				}
				if cursor == tc.first {
					feedRequire(t, unreadpg.NewReading(pool, nil, compose.NewTopicWriter(), nil).TopicShown(t.Context(), s, tc.joined, cursor, 1, cursor))
				}
			}
		})
	}
}

func testTopicReadBoundsScope(t *testing.T) {
	for _, predicate := range []string{"organization", "channel", "topic", "member"} {
		t.Run("topic/"+predicate, func(t *testing.T) {
			pool, scope, topic := shownFixture(t)
			s := unread.TopicScope{Scope: scope, TopicID: topic}
			other := s
			foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
			// As in the feed scope test, relax FKs only in this disposable database.
			_, err := pool.Exec(t.Context(), `ALTER TABLE message DROP CONSTRAINT message_topic_fkey, DROP CONSTRAINT message_organization_id_channel_id_fkey, DROP CONSTRAINT message_organization_id_member_id_fkey;
   ALTER TABLE channel_read DROP CONSTRAINT channel_read_organization_id_channel_id_fkey, DROP CONSTRAINT channel_read_organization_id_member_id_fkey;
   ALTER TABLE topic_read_floor DROP CONSTRAINT topic_read_floor_organization_id_channel_id_topic_id_fkey`)
			feedRequire(t, err)
			switch predicate {
			case "organization":
				other.OrganizationID = foreign.OrganizationID
			case "channel":
				other.ChannelID = conversationtest.Channel(t, pool, s.OrganizationID, "foreign", false).ID
			case "topic":
				other.TopicID = conversationtest.Topic(t, pool, s.OrganizationID, s.ChannelID, "foreign").ID
			case "member":
				other.MemberID = foreign.MemberID
			}
			_, err = pool.Exec(t.Context(), "DELETE FROM message WHERE organization_id=$1", s.OrganizationID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'own',n FROM unnest(ARRAY[20,30]) n", s.OrganizationID, s.ChannelID, s.TopicID, s.MemberID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", other.OrganizationID, other.ChannelID, other.MemberID)
			feedRequire(t, err)
			if predicate != "topic" {
				_, err = pool.Exec(t.Context(), "INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) VALUES ($1,$2,$3,31,32)", other.OrganizationID, other.ChannelID, other.MemberID)
				feedRequire(t, err)
			}
			if predicate != "channel" {
				_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,0)", other.OrganizationID, other.ChannelID, other.MemberID, other.TopicID)
				feedRequire(t, err)
			}
			if predicate != "member" {
				_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'foreign',10)", other.OrganizationID, other.ChannelID, other.TopicID, other.MemberID)
				feedRequire(t, err)
				feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
					q := conversationpg.TopicReadCandidatesIn(tx)
					ranges, err := q.ShownRanges(t.Context(), s.OrganizationID, s.ChannelID, s.TopicID, 41, 1, 30, 3, 2, nil)
					if err != nil {
						return err
					}
					if len(ranges) != 2 {
						t.Fatalf("foreign candidates: %v", ranges)
					}
					for _, floor := range []int64{2, 15} {
						if floor == 15 {
							_, err = pool.Exec(t.Context(), "UPDATE message SET moved_event_seq=18 WHERE organization_id=$1 AND channel_id=$2 AND topic_id=$3 AND event_seq=10", other.OrganizationID, other.ChannelID, other.TopicID)
							if err != nil {
								return err
							}
						}
						blocking, err := q.RunFloor(t.Context(), s.OrganizationID, s.ChannelID, s.TopicID, 20, 31, 3, floor, nil)
						if err != nil {
							return err
						}
						if blocking != 31 {
							t.Fatalf("foreign guard blocker=%d floor=%d", blocking, floor)
						}
					}
					return nil
				}))
			}
			before := readOracleRows(t, pool, nil, false)
			reading := unreadpg.NewReading(pool, nil, compose.NewTopicWriter(), nil)
			feedRequire(t, reading.TopicShown(t.Context(), s, 2, 41, 20, 20))
			feedRequire(t, reading.TopicShown(t.Context(), s, 2, 41, 20, 30))
			want := []conversation.SequenceRange{{Lo: 0, Hi: 31}}
			if predicate == "topic" {
				want = []conversation.SequenceRange{{Lo: 0, Hi: 3}, {Lo: 11, Hi: 31}}
			}
			feedRanges(t, pool, scope, want)
			after := readOracleRows(t, pool, nil, false)
			// Compare foreign rows by their complete JSON values, including the topic-only floor.
			for _, row := range before {
				if !slices.Contains(after, row) {
					t.Fatalf("foreign row changed: %s", row)
				}
			}
			topicFloor(t, pool, s, 30)
		})
	}
}
