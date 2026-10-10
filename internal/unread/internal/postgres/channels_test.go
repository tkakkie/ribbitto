package postgres

import (
	"math"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestChannelRanges(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "ranges", "general")
	for _, tc := range []struct {
		name string
		rows []unread.Range
		want unread.ChannelReadState
	}{
		{"no stored state uses join prefix", nil, unread.ChannelReadState{Prefix: 6, Gaps: []unread.Range{{Lo: 6, Hi: math.MaxInt64}}}},
		{"fragmented ranges", []unread.Range{{Lo: 0, Hi: 11}, {Lo: 12, Hi: 13}, {Lo: 14, Hi: 16}}, unread.ChannelReadState{Prefix: 11, Gaps: []unread.Range{{Lo: 11, Hi: 12}, {Lo: 13, Hi: 14}, {Lo: 16, Hi: math.MaxInt64}}}},
		{"one range", []unread.Range{{Lo: 0, Hi: 20}}, unread.ChannelReadState{Prefix: 20, Gaps: []unread.Range{{Lo: 20, Hi: math.MaxInt64}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := conversationtest.Channel(t, pool, f.OrganizationID, tc.name, false).ID
			if tc.rows != nil {
				seedRanges(t, pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel, MemberID: f.MemberID}, tc.rows)
			}
			require(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, err := ChannelRangesIn(s).Read(t.Context(), f.OrganizationID, f.MemberID, 5, []kernel.ID{channel})
				require(t, err)
				if !reflect.DeepEqual(got[channel], tc.want) {
					t.Fatalf("state=%v, want %v", got[channel], tc.want)
				}
				return nil
			}))
		})
	}
	t.Run("more than 101 stored ranges", func(t *testing.T) {
		rs := []unread.Range{{Lo: 0, Hi: 2}}
		for i := int64(1); i < 150; i++ {
			rs = append(rs, unread.Range{Lo: 2*i + 1, Hi: 2*i + 2})
		}
		seedRanges(t, pool, scopeOf(f), rs)
		require(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
			got, err := ChannelRangesIn(s).Read(t.Context(), f.OrganizationID, f.MemberID, 1, []kernel.ID{f.Channel.ID})
			require(t, err)
			state := got[f.Channel.ID]
			if state.Prefix != 2 || len(state.Gaps) != 100 || state.Gaps[99] != (unread.Range{Lo: 200, Hi: 201}) {
				t.Fatalf("truncated state=%v", state)
			}
			return nil
		}))
	})
}

func TestChannelRangesScope(t *testing.T) {
	for _, dimension := range []string{"organization", "member", "channel"} {
		t.Run(dimension, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
			scope := scopeOf(f)
			other := scope
			// Unique IDs and composite FKs otherwise mask single-predicate failures.
			_, err := pool.Exec(t.Context(), "ALTER TABLE channel_read DROP CONSTRAINT channel_read_organization_id_channel_id_fkey, DROP CONSTRAINT channel_read_organization_id_member_id_fkey")
			require(t, err)
			switch dimension {
			case "organization":
				other.OrganizationID = kernel.ID{1}
			case "member":
				other.MemberID = kernel.ID{1}
			case "channel":
				other.ChannelID = kernel.ID{1}
			}
			seedRanges(t, pool, other, []unread.Range{{Lo: 0, Hi: 50}, {Lo: 60, Hi: 70}})
			require(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, err := ChannelRangesIn(s).Read(t.Context(), scope.OrganizationID, scope.MemberID, 1, []kernel.ID{scope.ChannelID})
				require(t, err)
				want := unread.ChannelReadState{Prefix: 2, Gaps: []unread.Range{{Lo: 2, Hi: math.MaxInt64}}}
				if !reflect.DeepEqual(got, map[kernel.ID]unread.ChannelReadState{scope.ChannelID: want}) {
					t.Fatalf("single-scope decoy leaked: %v", got)
				}
				return nil
			}))
		})
	}
}

func TestChannelRangesStatementCount(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "statements", "general")
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	require(t, err)
	defer traced.Close()
	channels := []kernel.ID{f.Channel.ID}
	for i := range 10 {
		channels = append(channels, kernel.ID{byte(i + 1)})
	}
	require(t, platform.InSnapshot(t.Context(), traced, func(s platform.Snapshot) error {
		for _, ids := range [][]kernel.ID{channels[:1], channels} {
			before := counter.Counts().Queries
			got, err := ChannelRangesIn(s).Read(t.Context(), f.OrganizationID, f.MemberID, 1, ids)
			require(t, err)
			if len(got) != len(ids) || counter.Counts().Queries-before != 1 {
				t.Fatalf("%d channels: states=%d, statements=%d", len(ids), len(got), counter.Counts().Queries-before)
			}
		}
		return nil
	}))
}
