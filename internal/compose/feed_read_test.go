package compose_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func feedRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkedRead(t *testing.T, want bool) func(bool, error) error {
	t.Helper()
	return func(changed bool, err error) error {
		t.Helper()
		if (err == nil && changed != want) || (err != nil && changed) {
			return fmt.Errorf("changed=%t, want %t (error=%v)", changed, want, err)
		}
		return err
	}
}

func feedFixture(t *testing.T) (*pgxpool.Pool, unread.Scope) {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "feed", "general")
	posting := conversationpg.NewPosting(pool, compose.PostingSequence, compose.PostingEvents, nil)
	member := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	for range 5 {
		_, err := posting.Post(t.Context(), member, f.Channel.ID, "message")
		feedRequire(t, err)
	}
	return pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
}

func feedRanges(t *testing.T, pool *pgxpool.Pool, scope unread.Scope, want []conversation.SequenceRange) {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY lo", scope.OrganizationID, scope.ChannelID, scope.MemberID)
	feedRequire(t, err)
	defer rows.Close()
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversation.SequenceRange, error) {
		var r conversation.SequenceRange
		err := row.Scan(&r.Lo, &r.Hi)
		return r, err
	})
	feedRequire(t, err)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges=%v, want %v", got, want)
	}
}

func TestFeedRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                string
		joined, cursor, end int64
	}{
		{"message after snapshot stays unread", 1, 3, 4},
		{"no next message uses cursor plus one", 1, 6, 7},
		{"pre-join messages stay read with old cursor", 5, 0, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope := feedFixture(t)
			account := identitytest.Account(t, pool, "reader@example.org", "Reader")
			scope.MemberID = orgtest.Member(t, pool, scope.OrganizationID, account, org.RoleMember, "reader", tc.joined)
			feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				return checkedRead(t, tc.cursor > tc.joined)(compose.NewFeedWriter().Read(t.Context(), tx, scope, tc.joined, tc.cursor))
			}))
			feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: tc.end}})
		})
	}
}

func TestFeedReadReorderedAndRollback(t *testing.T) {
	t.Parallel()
	pool, scope := feedFixture(t)
	feed := compose.NewFeedWriter()
	for i, cursor := range []int64{5, 3, 1, 5} {
		feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return checkedRead(t, i == 0)(feed.Read(t.Context(), tx, scope, 1, cursor))
		}))
		feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: 6}})
	}
	rollback := errors.New("caller rollback")
	err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		if err := checkedRead(t, true)(feed.Read(t.Context(), tx, scope, 1, 6)); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: 6}})
}

func TestFeedReadRefusesFutureCursor(t *testing.T) {
	t.Parallel()
	pool, scope := feedFixture(t)
	// A second organisation has a higher cursor; it must not authorize this one.
	orgtest.Organization(t, pool, "other", "Other", 100)
	err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		return checkedRead(t, false)(compose.NewFeedWriter().Read(t.Context(), tx, scope, 1, 7))
	})
	if !errors.Is(err, unread.ErrInvalidCursor) {
		t.Fatalf("future cursor: %v", err)
	}
	feedRanges(t, pool, scope, []conversation.SequenceRange{})
}

func TestFeedReadConcurrent(t *testing.T) {
	t.Parallel()
	for _, cursors := range [][2]int64{{3, 5}, {5, 3}, {5, 5}} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d then %d existing=%t", cursors[0], cursors[1], exists), func(t *testing.T) {
				pool, scope := feedFixture(t)
				feed := compose.NewFeedWriter()
				if exists {
					feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
						return checkedRead(t, false)(feed.Read(t.Context(), tx, scope, 1, 1))
					}))
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				locked, release := make(chan struct{}), make(chan struct{})
				first, second := make(chan error, 1), make(chan error, 1)
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
					feedRequire(t, <-first)
				}()
				go func() {
					first <- platform.InTx(ctx, pool, func(tx platform.Tx) error {
						if err := checkedRead(t, true)(feed.Read(ctx, tx, scope, 1, cursors[0])); err != nil {
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
					second <- platform.InTx(ctx, pool, func(tx platform.Tx) error {
						return checkedRead(t, cursors[1] > cursors[0])(feed.Read(ctx, tx, scope, 1, cursors[1]))
					})
				}()
				// Release only when PostgreSQL proves the second feed transaction waits
				// on the first; this also exercises concurrent creation of the lock row.
				for {
					var blocked bool
					feedRequire(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid)) > 0)").Scan(&blocked))
					if blocked {
						break
					}
					select {
					case err := <-second:
						t.Fatalf("feed did not wait: %v", err)
					default:
					}
				}
				close(release)
				feedRequire(t, <-second)
				feedRanges(t, pool, scope, []conversation.SequenceRange{{Lo: 0, Hi: 6}})
			})
		}
	}
}

func TestEffectiveReadNoopsAndStatements(t *testing.T) {
	for _, kind := range []string{"feed", "topic"} {
		for _, tc := range []struct {
			name                         string
			joined, first, cursor, floor int64
			stored                       bool
		}{
			{"empty", 1, 0, 5, -1, false},
			{"before first message", 1, 0, 5, -1, false},
			{"join prefix", 10, 0, 10, 0, false},
			{"join prefix fresh", 10, 0, 10, -1, false},
			{"repeated", 1, 10, 10, -1, false},
			{"older", 1, 20, 5, -1, false},
			{"event only extension", 1, 10, 20, -1, false},
			{"identical range", 1, 0, 10, 0, true},
			{"floor only", 1, 0, 20, 10, false},
		} {
			if kind == "feed" && tc.name == "floor only" {
				continue
			}
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "noops", "general")
				s := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
				_, err := pool.Exec(t.Context(), "UPDATE organization SET event_seq=30,event_log_boundary_seq=30 WHERE id=$1", f.OrganizationID)
				feedRequire(t, err)
				if tc.name != "empty" {
					_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'read',10)", s.OrganizationID, s.ChannelID, f.Channel.DefaultTopicID, s.MemberID)
					feedRequire(t, err)
				}
				if tc.floor >= 0 {
					_, err = pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
					feedRequire(t, err)
					_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, s.ChannelID, s.MemberID, f.Channel.DefaultTopicID, tc.floor)
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
				read := func(cursor int64, want bool) {
					feedRequire(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
						before := counter.Counts().Queries
						if kind == "feed" {
							err = checkedRead(t, want)(compose.NewFeedWriter().Read(t.Context(), tx, s, tc.joined, cursor))
						} else {
							err = checkedRead(t, want)(compose.NewTopicWriter().Read(t.Context(), tx, unread.TopicScope{Scope: s, TopicID: f.Channel.DefaultTopicID}, tc.joined, cursor))
						}
						if err != nil {
							return err
						}
						got, wantStatements := counter.Counts().Queries-before, int64(12)
						if kind == "feed" {
							wantStatements = 10
							if !want && tc.name != "empty" && tc.name != "before first message" && tc.name != "event only extension" && tc.name != "identical range" {
								wantStatements = 9
							}
						}
						if tc.floor < 0 && (tc.first == 0 || want) {
							wantStatements += 4
						}
						if got != wantStatements {
							t.Fatalf("statements=%d, want %d", got, wantStatements)
						}
						t.Logf("cursor=%d changed=%t statements=%d", cursor, want, got)
						return err
					}))
				}
				if tc.first > 0 {
					read(tc.first, true)
				}
				read(tc.cursor, false)
			})
		}
	}
}

func TestFeedReadFragmentedCost(t *testing.T) {
	for _, match := range []bool{false, true} {
		t.Run(fmt.Sprint(match), func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "fragmented", "general")
			s := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
			for _, query := range []string{
				"UPDATE organization SET event_seq=20000,event_log_boundary_seq=20000 WHERE id=$1",
				"INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)",
				"INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) SELECT $1,$2,$3,i,i+1 FROM generate_series(2,20000,2) i",
			} {
				args := []any{s.OrganizationID, s.ChannelID, s.MemberID}
				if strings.HasPrefix(query, "UPDATE") {
					args = args[:1]
				}
				_, err := pool.Exec(t.Context(), query, args...)
				feedRequire(t, err)
			}
			_, err := pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'read',i FROM generate_series(2,20000,2) i", s.OrganizationID, s.ChannelID, f.Channel.DefaultTopicID, s.MemberID)
			feedRequire(t, err)
			if match {
				_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'unread',19999)", s.OrganizationID, s.ChannelID, f.Channel.DefaultTopicID, s.MemberID)
				feedRequire(t, err)
			}
			_, err = pool.Exec(t.Context(), "ANALYZE message")
			feedRequire(t, err)
			var set pgtype.Multirange[pgtype.Range[pgtype.Int8]]
			for lo := int64(3); lo < 20000; lo += 2 {
				set = append(set, pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: lo, Valid: true}, Upper: pgtype.Int8{Int64: lo + 1, Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true})
			}
			rows, err := pool.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) SELECT EXISTS (SELECT 1 FROM message WHERE organization_id=$1 AND channel_id=$2 AND $3::int8multirange @> event_seq)", s.OrganizationID, s.ChannelID, set)
			feedRequire(t, err)
			plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
			feedRequire(t, err)
			for _, line := range plan {
				if !strings.Contains(line, "Filter:") {
					t.Log(line)
				}
			}
			counter := platform.NewQueryCounter()
			config := pool.Config()
			config.ConnConfig.Tracer = counter
			traced, err := pgxpool.NewWithConfig(t.Context(), config)
			feedRequire(t, err)
			defer traced.Close()
			feedRequire(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
				before := counter.Counts().Queries
				if err := checkedRead(t, match)(compose.NewFeedWriter().Read(t.Context(), tx, s, 1, 20000)); err != nil {
					return err
				}
				if got := counter.Counts().Queries - before; got != 10 {
					t.Fatalf("fragmented read statements=%d, want 10", got)
				}
				return nil
			}))
		})
	}
}
