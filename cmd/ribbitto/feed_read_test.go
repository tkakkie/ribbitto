package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

func feedFixture(t *testing.T) (*pgxpool.Pool, unread.Scope) {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "feed", "general")
	posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	member := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	for range 5 {
		_, err := posting.Post(t.Context(), member, f.Channel.ID, "message")
		feedRequire(t, err)
	}
	return pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
}

func feedRanges(t *testing.T, pool *pgxpool.Pool, scope unread.Scope, want []unread.Range) {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY lo", scope.OrganizationID, scope.ChannelID, scope.MemberID)
	feedRequire(t, err)
	defer rows.Close()
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (unread.Range, error) {
		var r unread.Range
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
				return newFeedWriter().Read(t.Context(), tx, scope, tc.joined, tc.cursor)
			}))
			feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: tc.end}})
		})
	}
}

func TestFeedReadReorderedAndRollback(t *testing.T) {
	t.Parallel()
	pool, scope := feedFixture(t)
	feed := newFeedWriter()
	for _, cursor := range []int64{5, 3, 1, 5} {
		feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return feed.Read(t.Context(), tx, scope, 1, cursor)
		}))
		feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 6}})
	}
	rollback := errors.New("caller rollback")
	err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		if err := feed.Read(t.Context(), tx, scope, 1, 6); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 6}})
}

func TestFeedReadRefusesFutureCursor(t *testing.T) {
	t.Parallel()
	pool, scope := feedFixture(t)
	// A second organisation has a higher cursor; it must not authorize this one.
	orgtest.Organization(t, pool, "other", "Other", 100)
	err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		return newFeedWriter().Read(t.Context(), tx, scope, 1, 7)
	})
	if !errors.Is(err, unread.ErrInvalidCursor) {
		t.Fatalf("future cursor: %v", err)
	}
	feedRanges(t, pool, scope, []unread.Range{})
}

func TestFeedReadConcurrent(t *testing.T) {
	t.Parallel()
	for _, cursors := range [][2]int64{{3, 5}, {5, 3}} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d then %d existing=%t", cursors[0], cursors[1], exists), func(t *testing.T) {
				pool, scope := feedFixture(t)
				feed := newFeedWriter()
				if exists {
					feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
						return feed.Read(t.Context(), tx, scope, 1, 1)
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
						if err := feed.Read(ctx, tx, scope, 1, cursors[0]); err != nil {
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
						return feed.Read(ctx, tx, scope, 1, cursors[1])
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
				feedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 6}})
			})
		}
	}
}
