package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
)

func topicFloor(t *testing.T, pool *pgxpool.Pool, s unread.TopicScope, want int64) {
	t.Helper()
	var got int64
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT floor_seq FROM topic_read_floor WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND topic_id=$4", s.OrganizationID, s.ChannelID, s.MemberID, s.TopicID).Scan(&got))
	if got != want {
		t.Fatalf("floor=%d, want %d", got, want)
	}
}

func TestTopicReadExamples(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "examples", "general")
	scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "mio@example.org", "Mio"), org.RoleMember, "mio", 5)}
	topics := make(map[string]kernel.ID)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		topics[name] = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, name).ID
	}
	advance := func(t *testing.T, cursor int64) {
		_, err := pool.Exec(t.Context(), "UPDATE organization SET event_seq=$2,event_log_boundary_seq=$2 WHERE id=$1", f.OrganizationID, cursor)
		feedRequire(t, err)
	}
	insert := func(t *testing.T, topic string, seqs ...int64) {
		_, err := pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'example',unnest($5::bigint[])", f.OrganizationID, f.Channel.ID, topics[topic], f.MemberID, seqs)
		feedRequire(t, err)
	}
	read := func(t *testing.T, topic string, cursor int64) {
		s := unread.TopicScope{Scope: scope, TopicID: topics[topic]}
		feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error { return newTopicWriter().Read(t.Context(), tx, s, 5, cursor) }))
		topicFloor(t, pool, s, cursor)
	}
	brancher := conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, nil)
	move := func(t *testing.T, from, to string, seqs ...int64) {
		var ids []kernel.ID
		feedRequire(t, pool.QueryRow(t.Context(), "SELECT array_agg(id ORDER BY event_seq) FROM message WHERE organization_id=$1 AND event_seq=ANY($2::bigint[])", f.OrganizationID, seqs).Scan(&ids))
		target := topics[to]
		_, err := brancher.Branch(t.Context(), org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}, f.Channel.ID, conversation.Branch{From: topics[from], To: &target, Messages: ids}, func(conversation.Topic) string { return "moved" })
		feedRequire(t, err)
	}
	check := func(t *testing.T, want ...unread.Range) {
		feedRanges(t, pool, scope, want)
		for i := 1; i < len(want); i++ {
			var exists bool
			feedRequire(t, pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT FROM message WHERE organization_id=$1 AND channel_id=$2 AND event_seq >= $3 AND event_seq < $4)", scope.OrganizationID, scope.ChannelID, want[i-1].Hi, want[i].Lo).Scan(&exists))
			if !exists {
				t.Fatal("bounded gap has no unread message")
			}
		}
	}
	insert(t, "a", 10, 12, 14)
	insert(t, "b", 11, 13)
	insert(t, "c", 16)
	advance(t, 18)
	t.Run("Reading one topic (topic test 2)", func(t *testing.T) {
		read(t, "a", 18)
		check(t, unread.Range{Lo: 0, Hi: 11}, unread.Range{Lo: 12, Hi: 13}, unread.Range{Lo: 14, Hi: 16})
	})
	t.Run("Branching (topic test 1)", func(t *testing.T) {
		advance(t, 19)
		move(t, "a", "d", 12, 14)
		check(t, unread.Range{Lo: 0, Hi: 11}, unread.Range{Lo: 12, Hi: 13}, unread.Range{Lo: 14, Hi: 16})
	})
	t.Run("Moving an unread message into a topic read further; A message that arrives after the snapshot", func(t *testing.T) {
		advance(t, 24)
		read(t, "b", 24)
		move(t, "c", "b", 16)
		read(t, "b", 24)
		check(t, unread.Range{Lo: 0, Hi: 16})
	})
	t.Run("Moving a message more than once", func(t *testing.T) {
		move(t, "b", "d", 16)
		move(t, "d", "b", 12)
		check(t, unread.Range{Lo: 0, Hi: 16})
	})
	t.Run("Moving read and unread messages together", func(t *testing.T) {
		insert(t, "a", 31, 32)
		advance(t, 32)
		move(t, "a", "e", 10, 31)
		check(t, unread.Range{Lo: 0, Hi: 16})
	})
}

func TestTopicReadStatementsAndRollback(t *testing.T) {
	pool, scope := feedFixture(t)
	var topic kernel.ID
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&topic))
	s := unread.TopicScope{Scope: scope, TopicID: topic}
	_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'candidate',s FROM generate_series(7,301) s;`, scope.OrganizationID, scope.ChannelID, topic, scope.MemberID)
	feedRequire(t, err)
	_, err = pool.Exec(t.Context(), "UPDATE organization SET event_seq=301,event_log_boundary_seq=301 WHERE id=$1", scope.OrganizationID)
	feedRequire(t, err)
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	feedRequire(t, err)
	defer traced.Close()
	rollback := errors.New("caller rollback")
	for _, cursor := range []int64{1, 2, 301} {
		err = platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
			before := counter.Counts().Queries
			if err := newTopicWriter().Read(t.Context(), tx, s, 1, cursor); err != nil {
				return err
			}
			got := counter.Counts().Queries - before
			if got != 16 {
				t.Fatalf("cursor %d: %d statements, want 16", cursor, got)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		feedRanges(t, pool, scope, []unread.Range{})
		var rows int
		feedRequire(t, pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM channel_read)+(SELECT count(*) FROM topic_read_floor)").Scan(&rows))
		if rows != 0 {
			t.Fatal("lock or floor escaped rollback")
		}
	}
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	s = unread.TopicScope{Scope: unread.Scope{OrganizationID: other.OrganizationID, ChannelID: other.Channel.ID, MemberID: other.MemberID}, TopicID: other.Channel.DefaultTopicID}
	err = platform.InTx(t.Context(), pool, func(tx platform.Tx) error { return newTopicWriter().Read(t.Context(), tx, s, 1, 2) })
	if !errors.Is(err, unread.ErrInvalidCursor) {
		t.Fatalf("future cursor: %v", err)
	}
	feedRanges(t, pool, scope, []unread.Range{})
}

func TestTopicAndFeedReadConcurrentAndReordered(t *testing.T) {
	for _, kinds := range [][2]string{{"topic", "topic"}, {"topic", "feed"}, {"feed", "topic"}} {
		for _, cursors := range [][2]int64{{3, 5}, {5, 3}} {
			t.Run("Two tabs at once/"+fmt.Sprint(kinds, cursors), func(t *testing.T) {
				pool, scope := feedFixture(t)
				var topic kernel.ID
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&topic))
				s := unread.TopicScope{Scope: scope, TopicID: topic}
				other := conversationtest.Topic(t, pool, scope.OrganizationID, scope.ChannelID, "other")
				_, err := pool.Exec(t.Context(), "UPDATE message SET topic_id=$2 WHERE organization_id=$1 AND event_seq=4", scope.OrganizationID, other.ID)
				feedRequire(t, err)
				want := []unread.Range{{Lo: 0, Hi: 4}, {Lo: 5, Hi: 6}}
				if (kinds[0] == "feed" && cursors[0] == 5) || (kinds[1] == "feed" && cursors[1] == 5) {
					want = []unread.Range{{Lo: 0, Hi: 6}}
				}
				write := func(ctx context.Context, tx platform.Tx, kind string, cursor int64) error {
					if kind == "feed" {
						return newFeedWriter().Read(ctx, tx, scope, 1, cursor)
					}
					return newTopicWriter().Read(ctx, tx, s, 1, cursor)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				locked, release := make(chan struct{}), make(chan struct{})
				first, second := make(chan error, 1), make(chan error, 1)
				unblock := sync.OnceFunc(func() { close(release) })
				defer func() { unblock(); feedRequire(t, <-first) }()
				go func() {
					first <- platform.InTx(ctx, pool, func(tx platform.Tx) error {
						if err := write(ctx, tx, kinds[0], cursors[0]); err != nil {
							return err
						}
						close(locked)
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
				}()
				select {
				case <-locked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				go func() {
					second <- platform.InTx(ctx, pool, func(tx platform.Tx) error { return write(ctx, tx, kinds[1], cursors[1]) })
				}()
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
				unblock()
				feedRequire(t, <-second)
				feedRanges(t, pool, scope, want)
				// An absent floor after a feed read starts at the prefix minus one.
				floor := cursors[0]
				if kinds[1] == "topic" {
					floor = max(floor, cursors[1])
				}
				topicFloor(t, pool, s, floor)
				for _, kind := range []string{"topic", "feed", "topic"} {
					feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error { return write(t.Context(), tx, kind, 1) }))
					feedRanges(t, pool, scope, want)
					topicFloor(t, pool, s, floor)
				}
			})
		}
	}
}
