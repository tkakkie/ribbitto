package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func floorValue(t *testing.T, pool *pgxpool.Pool, s unread.Scope, topic kernel.ID) int64 {
	t.Helper()
	var floor int64
	require(t, pool.QueryRow(t.Context(), "SELECT floor_seq FROM topic_read_floor WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND topic_id=$4", s.OrganizationID, s.ChannelID, s.MemberID, topic).Scan(&floor))
	return floor
}
func seedFloor(t *testing.T, pool *pgxpool.Pool, s unread.Scope, topic kernel.ID, floor int64) {
	t.Helper()
	_, err := pool.Exec(t.Context(), "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,$5)", s.OrganizationID, topic, s.MemberID, s.ChannelID, floor)
	require(t, err)
}

func TestTopicBatch(t *testing.T) {
	many := []unread.Range{{Lo: 0, Hi: 2}}
	for i := int64(10); i < 310; i += 3 {
		many = append(many, unread.Range{Lo: i, Hi: i + 1})
	}
	tests := []struct {
		name                string
		stored, added, want []unread.Range
	}{
		{"first use", nil, nil, []unread.Range{{Lo: 0, Hi: 6}}},
		{"both neighbours", []unread.Range{{Lo: 0, Hi: 2}, {Lo: 8, Hi: 12}, {Lo: 20, Hi: 22}}, []unread.Range{{Lo: 12, Hi: 20}}, []unread.Range{{Lo: 0, Hi: 6}, {Lo: 8, Hi: 22}}},
		{"touching additions", nil, []unread.Range{{Lo: 12, Hi: 15}, {Lo: 10, Hi: 12}}, []unread.Range{{Lo: 0, Hi: 6}, {Lo: 10, Hi: 15}}},
		{"stored connector", []unread.Range{{Lo: 0, Hi: 2}, {Lo: 12, Hi: 20}}, []unread.Range{{Lo: 10, Hi: 13}, {Lo: 19, Hi: 22}}, []unread.Range{{Lo: 0, Hi: 6}, {Lo: 10, Hi: 22}}},
		{"join prefix", []unread.Range{{Lo: 0, Hi: 2}, {Lo: 5, Hi: 9}}, []unread.Range{{Lo: 9, Hi: 12}}, []unread.Range{{Lo: 0, Hi: 12}}},
		{"many stored", many, []unread.Range{{Lo: 6, Hi: 310}}, []unread.Range{{Lo: 0, Hi: 310}}},
		{"many additions", nil, many[1:], append([]unread.Range{{Lo: 0, Hi: 6}}, many[1:]...)},
	}
	for _, tt := range tests {
		for _, floor := range []int64{-1, 5, 10, 20} {
			t.Run(tt.name+"/"+map[int64]string{-1: "absent", 5: "lower", 10: "equal", 20: "higher"}[floor], func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "batch", "general")
				s, topic := scopeOf(f), f.Channel.DefaultTopicID
				if tt.stored != nil {
					seedRanges(t, pool, s, tt.stored)
				} else if floor != -1 {
					seedRanges(t, pool, s, nil)
				}
				if floor != -1 {
					seedFloor(t, pool, s, topic, floor)
				}
				counter := platform.NewQueryCounter()
				config := pool.Config()
				config.ConnConfig.Tracer = counter
				traced, err := pgxpool.NewWithConfig(t.Context(), config)
				require(t, err)
				defer traced.Close()
				require(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
					p, err := WriterIn(tx).Prepare(t.Context(), unread.TopicScope{Scope: s, TopicID: topic}, 5)
					require(t, err)
					prefix := int64(6)
					if tt.name == "join prefix" {
						prefix = 9
					}
					wantFloor := floor
					if floor == -1 {
						wantFloor = prefix - 1
					}
					if p.Prefix != prefix || p.Floor != wantFloor {
						t.Fatalf("prepared prefix/floor=%d/%d, want %d/%d", p.Prefix, p.Floor, prefix, wantFloor)
					}
					before := counter.Counts()
					if err := p.Add(t.Context(), []int64{10}, nil, 10); err == nil || counter.Counts() != before {
						t.Fatal("unequal arrays ran a statement or succeeded")
					}
					los, his := bounds(tt.added)
					require(t, p.Add(t.Context(), los, his, 10))
					if got := counter.Counts().Queries - before.Queries; got != 4 {
						t.Fatalf("batch of %d: %d statements, want 4", len(los), got)
					}
					return nil
				}))
				if got := ranges(t, pool, s); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("ranges=%v want %v", got, tt.want)
				}
				if got := floorValue(t, pool, s, topic); got != max(floor, 10) {
					t.Fatalf("floor=%d", got)
				}
				rollback := errors.New("caller rollback")
				err = platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
					p, err := WriterIn(tx).Prepare(t.Context(), unread.TopicScope{Scope: s, TopicID: topic}, 5)
					require(t, err)
					require(t, p.Add(t.Context(), []int64{1000}, []int64{1001}, 2000))
					return rollback
				})
				if !errors.Is(err, rollback) || !reflect.DeepEqual(ranges(t, pool, s), tt.want) || floorValue(t, pool, s, topic) != max(floor, 10) {
					t.Fatal("writes escaped rollback")
				}
			})
		}
	}
}

// An absent floor reads as Prefix - 1; a cursor below it must not lower it.
func TestTopicFloorFallback(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "fallback", "general")
	s, topic := scopeOf(f), f.Channel.DefaultTopicID
	require(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		p, err := WriterIn(tx).Prepare(t.Context(), unread.TopicScope{Scope: s, TopicID: topic}, 5)
		require(t, err)
		return p.Add(t.Context(), nil, nil, 2)
	}))
	if got := floorValue(t, pool, s, topic); got != 5 {
		t.Fatalf("floor=%d, want the fallback 5", got)
	}
}

func TestTopicBatchScope(t *testing.T) {
	for _, dimension := range []string{"organization", "channel", "member", "topic"} {
		for _, existing := range []bool{false, true} {
			t.Run(dimension+map[bool]string{false: "/absent", true: "/existing"}[existing], func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
				s, topic := scopeOf(f), f.Channel.DefaultTopicID
				other, otherTopic := s, topic
				// Unique foreign IDs cannot differ in just organization/channel. Drop only
				// those FKs in this disposable database to isolate each query predicate.
				_, err := pool.Exec(t.Context(), "ALTER TABLE channel_read DROP CONSTRAINT channel_read_organization_id_channel_id_fkey, DROP CONSTRAINT channel_read_organization_id_member_id_fkey; ALTER TABLE topic_read_floor DROP CONSTRAINT topic_read_floor_organization_id_channel_id_topic_id_fkey")
				require(t, err)
				switch dimension {
				case "organization":
					other.OrganizationID = kernel.ID{}
				case "channel":
					other.ChannelID = kernel.ID{}
					_, err = pool.Exec(t.Context(), "ALTER TABLE topic_read_floor DROP CONSTRAINT topic_read_floor_pkey")
					require(t, err)
				case "member":
					other.MemberID = kernel.ID{}
				case "topic":
					otherTopic = kernel.ID{}
				}
				old := []unread.Range{{Lo: 0, Hi: 2}, {Lo: 9, Hi: 10}, {Lo: 20, Hi: 99}}
				if dimension != "topic" {
					seedRanges(t, pool, other, old)
				}
				seedRanges(t, pool, s, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 8, Hi: 12}, {Lo: 20, Hi: 21}})
				seedFloor(t, pool, other, otherTopic, 4)
				if existing {
					seedFloor(t, pool, s, topic, 5)
				}
				require(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
					p, err := WriterIn(tx).Prepare(t.Context(), unread.TopicScope{Scope: s, TopicID: topic}, 1)
					require(t, err)
					wantFloor := int64(1)
					if existing {
						wantFloor = 5
					}
					if p.Prefix != 2 || p.Floor != wantFloor || !reflect.DeepEqual(p.Ranges, []unread.Range{{Lo: 8, Hi: 12}, {Lo: 20, Hi: 21}}) {
						t.Fatalf("unscoped preparation: %+v", p)
					}
					return p.Add(t.Context(), []int64{10}, []int64{20}, 10)
				}))
				if got := ranges(t, pool, s); !reflect.DeepEqual(got, []unread.Range{{Lo: 0, Hi: 2}, {Lo: 8, Hi: 21}}) {
					t.Fatalf("scoped union=%v", got)
				}
				if dimension != "topic" && !reflect.DeepEqual(ranges(t, pool, other), old) {
					t.Fatal("foreign ranges changed")
				}
				if floorValue(t, pool, other, otherTopic) != 4 || floorValue(t, pool, s, topic) != 10 {
					t.Fatal("unscoped floor write")
				}
			})
		}
	}
}
