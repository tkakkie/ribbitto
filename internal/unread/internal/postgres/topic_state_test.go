package postgres

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestTopicState(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "no stored state"
		if stored {
			name = "ranges above and below P, topics with and without floor, unlisted selected topic"
		}
		t.Run(name, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "state", "general")
			scope := scopeOf(f)
			a := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "a").ID
			b := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "b").ID
			selected := f.Channel.DefaultTopicID
			want := unread.TopicReadState{Prefix: 6, Floors: map[kernel.ID]int64{a: 5, b: 5, selected: 5}}
			if stored {
				seedRanges(t, pool, scope, []unread.Range{{Lo: 0, Hi: 11}, {Lo: 12, Hi: 13}, {Lo: 14, Hi: 16}})
				seedFloor(t, pool, scope, selected, 18)
				want.Prefix, want.Ranges = 11, []unread.Range{{Lo: 12, Hi: 13}, {Lo: 14, Hi: 16}}
				want.Floors = map[kernel.ID]int64{a: 10, b: 10, selected: 18}
			}
			require(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				for _, listed := range [][]kernel.ID{{a, b, a}, {a, selected, b, selected}} {
					got, err := TopicStateIn(s).Read(t.Context(), scope, 5, listed, &selected)
					require(t, err)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("state=%v, want %v", got, want)
					}
				}
				return nil
			}))
		})
	}
}

func TestTopicStateScope(t *testing.T) {
	for _, source := range []string{"ranges", "floors"} {
		for _, dimension := range []string{"organization", "channel", "member", "topic"} {
			if source == "ranges" && dimension == "topic" {
				continue
			}
			t.Run(source+"/"+dimension, func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
				scope, other := scopeOf(f), scopeOf(f)
				topic, otherTopic := f.Channel.DefaultTopicID, f.Channel.DefaultTopicID
				// Composite FKs and globally unique IDs would mask removed predicates.
				_, err := pool.Exec(t.Context(), "ALTER TABLE channel_read DROP CONSTRAINT channel_read_organization_id_channel_id_fkey, DROP CONSTRAINT channel_read_organization_id_member_id_fkey")
				require(t, err)
				_, err = pool.Exec(t.Context(), "ALTER TABLE topic_read_floor DROP CONSTRAINT topic_read_floor_organization_id_channel_id_topic_id_fkey")
				require(t, err)
				switch dimension {
				case "organization":
					other.OrganizationID = kernel.ID{1}
				case "channel":
					other.ChannelID = kernel.ID{1}
				case "member":
					other.MemberID = kernel.ID{1}
				case "topic":
					otherTopic = kernel.ID{1}
				}
				if source == "ranges" {
					seedRanges(t, pool, other, []unread.Range{{Lo: 0, Hi: 50}, {Lo: 60, Hi: 70}})
				} else {
					seedRanges(t, pool, other, nil)
					seedFloor(t, pool, other, otherTopic, 99)
				}
				require(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
					got, err := TopicStateIn(s).Read(t.Context(), scope, 5, []kernel.ID{topic}, nil)
					require(t, err)
					want := unread.TopicReadState{Prefix: 6, Floors: map[kernel.ID]int64{topic: 5}}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("single-scope decoy leaked: %v", got)
					}
					return nil
				}))
			})
		}
	}
}

func TestTopicStateStatementCount(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "statements", "general")
	seedRanges(t, pool, scopeOf(f), []unread.Range{{Lo: 0, Hi: 11}, {Lo: 12, Hi: 13}})
	seedFloor(t, pool, scopeOf(f), f.Channel.DefaultTopicID, 18)
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	require(t, err)
	defer traced.Close()
	topics := []kernel.ID{f.Channel.DefaultTopicID}
	for i := range 50 {
		topics = append(topics, kernel.ID{byte(i + 1)})
	}
	require(t, platform.InSnapshot(t.Context(), traced, func(s platform.Snapshot) error {
		for _, n := range []int{1, 51} {
			t.Run(map[int]string{1: "1 topic", 51: "51 topics"}[n], func(t *testing.T) {
				listed := append(append([]kernel.ID(nil), topics[:n-1]...), topics[:n-1]...)
				before := counter.Counts().Queries
				got, err := TopicStateIn(s).Read(t.Context(), scopeOf(f), 5, listed, &topics[n-1])
				require(t, err)
				if len(got.Floors) != n || len(got.Ranges) != 1 || counter.Counts().Queries-before != 1 {
					t.Fatalf("%d topics: state=%v, statements=%d", n, got, counter.Counts().Queries-before)
				}
			})
		}
		return nil
	}))
}
