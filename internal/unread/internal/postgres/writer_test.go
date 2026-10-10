package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func scopeOf(f conversationtest.OrganizationFixture) unread.Scope {
	return unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
}
func ranges(t *testing.T, pool *pgxpool.Pool, s unread.Scope) []unread.Range {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY lo", s.OrganizationID, s.ChannelID, s.MemberID)
	require(t, err)
	defer rows.Close()
	result, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (unread.Range, error) {
		var r unread.Range
		err := row.Scan(&r.Lo, &r.Hi)
		return r, err
	})
	require(t, err)
	return result
}
func merge(t *testing.T, pool *pgxpool.Pool, s unread.Scope, r unread.Range) {
	t.Helper()
	require(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error { return WriterIn(tx).Merge(t.Context(), s, 1, r) }))
}

func TestMergeRanges(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	s := scopeOf(conversationtest.OrganizationWithOwner(t, pool, "ranges", "general"))
	tests := []struct {
		name  string
		added unread.Range
		want  []unread.Range
	}{
		{"first write inserts prefix", unread.Range{Lo: 10, Hi: 12}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 12}}},
		{"disjoint", unread.Range{Lo: 20, Hi: 22}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 12}, {Lo: 20, Hi: 22}}},
		{"touch left", unread.Range{Lo: 12, Hi: 14}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 14}, {Lo: 20, Hi: 22}}},
		{"touch right", unread.Range{Lo: 18, Hi: 20}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 14}, {Lo: 18, Hi: 22}}},
		{"overlap", unread.Range{Lo: 19, Hi: 24}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 14}, {Lo: 18, Hi: 24}}},
		{"contained", unread.Range{Lo: 11, Hi: 13}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 14}, {Lo: 18, Hi: 24}}},
		{"bridge neighbours", unread.Range{Lo: 14, Hi: 18}, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 24}}},
		{"merge prefix", unread.Range{Lo: 2, Hi: 10}, []unread.Range{{Lo: 0, Hi: 24}}},
		{"reordered old range", unread.Range{Lo: 3, Hi: 4}, []unread.Range{{Lo: 0, Hi: 24}}},
	}
	before := []unread.Range{{Lo: 0, Hi: 2}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merge(t, pool, s, tt.added)
			got := ranges(t, pool, s)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ranges=%v, want %v", got, tt.want)
			}
			if got[0].Lo != 0 {
				t.Fatal("missing prefix")
			}
			for i, r := range got {
				if i > 0 && got[i-1].Hi >= r.Lo {
					t.Fatal("ranges overlap or touch")
				}
			}
			for _, old := range before {
				for seq := old.Lo; seq < old.Hi; seq++ {
					found := false
					for _, r := range got {
						found = found || r.Lo <= seq && seq < r.Hi
					}
					if !found {
						t.Fatalf("removed read sequence %d", seq)
					}
				}
			}
			before = got
		})
	}
	rollback := errors.New("rollback")
	err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		if err := WriterIn(tx).Merge(t.Context(), s, 1, unread.Range{Lo: 30, Hi: 31}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || !reflect.DeepEqual(ranges(t, pool, s), before) {
		t.Fatal("writes escaped caller rollback")
	}
}

func TestMergeScope(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
	s := scopeOf(f)
	// Globally unique channel/member IDs make an organisation-only adversary
	// impossible under these FKs. Remove only unread's FKs in this disposable
	// database to prove its queries carry their own organisation predicate.
	_, err := pool.Exec(t.Context(), "ALTER TABLE channel_read DROP CONSTRAINT channel_read_organization_id_channel_id_fkey, DROP CONSTRAINT channel_read_organization_id_member_id_fkey")
	require(t, err)
	others := []unread.Scope{s, s, s}
	others[0].OrganizationID = kernel.ID{}
	others[1].ChannelID = conversationtest.Channel(t, pool, s.OrganizationID, "other", false).ID
	account := identitytest.Account(t, pool, "other@example.org", "Other")
	others[2].MemberID = orgtest.Member(t, pool, s.OrganizationID, account, org.RoleMember, "other", 1)
	for _, other := range others {
		seedRanges(t, pool, other, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 9, Hi: 99}})
	}
	seedRanges(t, pool, s, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 8, Hi: 12}, {Lo: 20, Hi: 21}})
	require(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		require(t, WriterIn(tx).Merge(t.Context(), s, 1, unread.Range{Lo: 10, Hi: 11}))
		// Range writes also lock rows; probe channel_read itself so those writes
		// cannot conceal a missing or incorrectly scoped channel lock.
		err := pgx.BeginFunc(t.Context(), pool, func(probe pgx.Tx) error {
			_, err := probe.Exec(t.Context(), "SELECT FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 FOR UPDATE NOWAIT", s.OrganizationID, s.ChannelID, s.MemberID)
			return err
		})
		var lock *pgconn.PgError
		if !errors.As(err, &lock) || lock.Code != "55P03" {
			t.Fatalf("channel lock not held: %v", err)
		}
		for i, other := range others {
			err := pgx.BeginFunc(t.Context(), pool, func(probe pgx.Tx) error {
				_, err := probe.Exec(t.Context(), "SELECT FROM channel_read WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 FOR UPDATE NOWAIT", other.OrganizationID, other.ChannelID, other.MemberID)
				return err
			})
			if err != nil {
				t.Fatalf("scope %d locked: %v", i, err)
			}
		}
		return nil
	}))
	if got := ranges(t, pool, s); !reflect.DeepEqual(got, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 8, Hi: 12}, {Lo: 20, Hi: 21}}) {
		t.Fatalf("scoped ranges=%v", got)
	}
	for i, other := range others {
		if got := ranges(t, pool, other); !reflect.DeepEqual(got, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 9, Hi: 99}}) {
			t.Fatalf("scope %d changed: %v", i, got)
		}
	}
}

func seedRanges(t *testing.T, pool *pgxpool.Pool, s unread.Scope, rs []unread.Range) {
	t.Helper()
	_, err := pool.Exec(t.Context(), "INSERT INTO channel_read VALUES ($1,$2,$3)", s.OrganizationID, s.ChannelID, s.MemberID)
	require(t, err)
	for _, r := range rs {
		_, err = pool.Exec(t.Context(), "INSERT INTO read_range VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, s.ChannelID, s.MemberID, r.Lo, r.Hi)
		require(t, err)
	}
}

func TestConcurrentMerges(t *testing.T) {
	t.Parallel()
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "create lock", true: "existing lock"}[exists], func(t *testing.T) {
			pool := pgtest.New(t)
			s := scopeOf(conversationtest.OrganizationWithOwner(t, pool, "concurrent", "general"))
			if exists {
				seedRanges(t, pool, s, []unread.Range{{Lo: 0, Hi: 2}})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			locked := make(chan uint32, 1)
			release := make(chan struct{})
			first := make(chan error, 1)
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				require(t, <-first)
			}()
			go func() {
				first <- platform.InTx(ctx, pool, func(tx platform.Tx) error {
					if err := WriterIn(tx).Merge(ctx, s, 1, unread.Range{Lo: 10, Hi: 11}); err != nil {
						return err
					}
					locked <- pgxbridge.Tx(tx).Conn().PgConn().PID()
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			var holder uint32
			select {
			case holder = <-locked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			pid := make(chan uint32, 1)
			second := make(chan error, 1)
			go func() {
				second <- platform.InTx(ctx, pool, func(tx platform.Tx) error {
					pid <- pgxbridge.Tx(tx).Conn().PgConn().PID()
					return WriterIn(tx).Merge(ctx, s, 1, unread.Range{Lo: 11, Hi: 12})
				})
			}()
			waiter := <-pid
			// Poll database state, not elapsed time: release only after PostgreSQL
			// proves the second transaction is waiting on the first transaction.
			for {
				var blocked bool
				require(t, pool.QueryRow(ctx, "SELECT $1 = ANY(pg_blocking_pids($2))", holder, waiter).Scan(&blocked))
				if blocked {
					break
				}
				select {
				case err := <-second:
					t.Fatalf("merge did not wait: %v", err)
				default:
				}
			}
			close(release)
			require(t, <-second)
			if got := ranges(t, pool, s); !reflect.DeepEqual(got, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 10, Hi: 12}}) {
				t.Fatalf("concurrent union=%v", got)
			}
		})
	}
}

func TestReadStateForeignKeys(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	a := conversationtest.OrganizationWithOwner(t, pool, "a", "general")
	b := conversationtest.OrganizationWithOwner(t, pool, "b", "general")
	s := scopeOf(a)
	merge(t, pool, s, unread.Range{Lo: 10, Hi: 11})
	other := conversationtest.Channel(t, pool, a.OrganizationID, "other", false)
	tests := []struct {
		name, sql string
		args      []any
	}{
		{"channel organization", "INSERT INTO channel_read VALUES ($1,$2,$3)", []any{a.OrganizationID, b.Channel.ID, a.MemberID}},
		{"member organization", "INSERT INTO channel_read VALUES ($1,$2,$3)", []any{a.OrganizationID, a.Channel.ID, b.MemberID}},
		{"range lock organization", "INSERT INTO read_range VALUES ($1,$2,$3,0,1)", []any{b.OrganizationID, a.Channel.ID, a.MemberID}},
		{"topic organization", "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,1)", []any{a.OrganizationID, b.Channel.DefaultTopicID, a.MemberID, a.Channel.ID}},
		{"topic channel", "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,1)", []any{a.OrganizationID, other.DefaultTopicID, a.MemberID, a.Channel.ID}},
		{"floor member organization", "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,1)", []any{a.OrganizationID, a.Channel.DefaultTopicID, b.MemberID, a.Channel.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), tt.sql, tt.args...)
			var fk *pgconn.PgError
			if !errors.As(err, &fk) || fk.Code != "23503" {
				t.Fatalf("expected FK rejection, got %v", err)
			}
		})
	}
	_, err := pool.Exec(t.Context(), "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,1)", a.OrganizationID, a.Channel.DefaultTopicID, a.MemberID, a.Channel.ID)
	require(t, err)
}
