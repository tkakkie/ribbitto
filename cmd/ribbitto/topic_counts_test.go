package main

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
	"testing"
)

func TestTopicCounts(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		messages, prefix, floor, moved, want, first int64
		read                                        []unread.Range
	}{
		{name: "count 0", messages: 1, prefix: 3, floor: 1},
		{name: "count 1", messages: 1, prefix: 2, floor: 1, want: 1, first: 2},
		{name: "count 99", messages: 99, prefix: 2, floor: 1, want: 99, first: 2},
		{name: "count 100 capped", messages: 150, prefix: 2, floor: 1, want: 100, first: 2},
		{name: "both branches exceed shared cap", messages: 200, prefix: 2, floor: 101, moved: 100, want: 100, first: 2},
		{name: "prefix read moved above floor stays read", messages: 1, prefix: 51, floor: 100, moved: 1},
		{name: "first unread beyond 100 moved candidates", messages: 250, prefix: 2, floor: 251, moved: 250, want: 100, first: 3, read: []unread.Range{{Lo: 2, Hi: 3}}},
		{name: "fragmented read set with prefix read moves", messages: 12, prefix: 5, floor: 8, moved: 7, want: 6, first: 5, read: []unread.Range{{Lo: 6, Hi: 7}, {Lo: 9, Hi: 10}, {Lo: 12, Hi: 13}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "topics", "general")
			topic := f.Channel.DefaultTopicID
			_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq)
 SELECT $1,$2,$3,$4,'candidate',s+1,CASE WHEN s <= $6 THEN 10000-s END FROM generate_series(1,$5::bigint) s`, f.OrganizationID, f.Channel.ID, topic, f.MemberID, tc.messages, tc.moved)
			feedRequire(t, err)
			scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
			feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				w := unreadpg.WriterIn(tx)
				if err := w.Merge(t.Context(), scope, 1, unread.Range{Lo: 0, Hi: tc.prefix}); err != nil {
					return err
				}
				for _, r := range tc.read {
					if err := w.Merge(t.Context(), scope, 1, r); err != nil {
						return err
					}
				}
				return nil
			}))
			_, err = pool.Exec(t.Context(), "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,$5)", f.OrganizationID, topic, f.MemberID, f.Channel.ID, tc.floor)
			feedRequire(t, err)
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, first, err := newTopicCounts().Read(t.Context(), s, scope, 1, []kernel.ID{topic}, &topic)
				feedRequire(t, err)
				if got[topic] != tc.want || first != tc.first {
					t.Fatalf("count=%v first=%d; want %d/%d", got, first, tc.want, tc.first)
				}
				return nil
			}))
		})
	}
}

func TestTopicCountsReadingOneTopicAndMovesTwice(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "examples", "general")
	a := f.Channel.DefaultTopicID
	b := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "b").ID
	scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
	for _, m := range []struct {
		seq   int64
		topic kernel.ID
	}{{10, a}, {11, b}, {12, a}, {13, b}, {14, a}} {
		_, err := pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'example',$5)", f.OrganizationID, f.Channel.ID, m.topic, f.MemberID, m.seq)
		feedRequire(t, err)
	}
	check := func(name string, wantA, wantB, first int64) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, seq, err := newTopicCounts().Read(t.Context(), s, scope, 5, []kernel.ID{a, b}, &a)
				feedRequire(t, err)
				if got[a] != wantA || got[b] != wantB || seq != first {
					t.Fatalf("counts=%v first=%d; want %d/%d/%d", got, seq, wantA, wantB, first)
				}
				return nil
			}))
		})
	}
	check("before reading A", 3, 2, 10)
	feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		for _, r := range []unread.Range{{Lo: 0, Hi: 11}, {Lo: 12, Hi: 13}, {Lo: 14, Hi: 15}} {
			if err := unreadpg.WriterIn(tx).Merge(t.Context(), scope, 5, r); err != nil {
				return err
			}
		}
		return nil
	}))
	_, err := pool.Exec(t.Context(), "INSERT INTO topic_read_floor VALUES ($1,$2,$3,$4,18)", f.OrganizationID, a, f.MemberID, f.Channel.ID)
	feedRequire(t, err)
	check("reading A leaves B unchanged", 0, 2, 0)
	// Move read 12 and unread 13 together, twice; only the latest move survives.
	for i, to := range []kernel.ID{b, a} {
		_, err = pool.Exec(t.Context(), "UPDATE message SET topic_id=$3,moved_event_seq=$4 WHERE organization_id=$1 AND channel_id=$2 AND event_seq IN (12,13)", f.OrganizationID, f.Channel.ID, to, 20+i)
		feedRequire(t, err)
		if i == 0 {
			check("read and unread moved once", 0, 2, 0)
		} else {
			check("read and unread moved twice", 1, 1, 13)
		}
	}
}

func TestTopicCountsScope(t *testing.T) {
	for _, dimension := range []string{"organization", "channel", "topic"} {
		t.Run(dimension, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
			organization, channel, topic := f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID
			// Globally unique IDs and composite FKs otherwise hide missing predicates.
			_, err := pool.Exec(t.Context(), `ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey,
 DROP CONSTRAINT message_topic_fkey, DROP CONSTRAINT message_organization_id_member_id_fkey`)
			feedRequire(t, err)
			switch dimension {
			case "organization":
				organization = kernel.ID{1}
			case "channel":
				channel = kernel.ID{1}
			case "topic":
				topic = kernel.ID{1}
			}
			_, err = pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq)
 VALUES ($1,$2,$3,$4,'above',30,NULL),($1,$2,$3,$4,'moved',10,40)`, organization, channel, topic, f.MemberID)
			feedRequire(t, err)
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, first, err := conversationpg.TopicUnreadIn(s).Count(t.Context(), f.OrganizationID, f.Channel.ID, 2, []kernel.ID{f.Channel.DefaultTopicID}, []int64{20}, nil, &f.Channel.DefaultTopicID)
				feedRequire(t, err)
				if got[f.Channel.DefaultTopicID] != 0 || first != 0 {
					t.Fatalf("single-scope decoys leaked: %v first=%d", got, first)
				}
				return nil
			}))
		})
	}
}

func TestTopicCountsStatements(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "statements", "general")
	ids := []kernel.ID{f.Channel.DefaultTopicID}
	for i := range 50 {
		ids = append(ids, conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprint(i)).ID)
	}
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	feedRequire(t, err)
	defer traced.Close()
	feedRequire(t, platform.InSnapshot(t.Context(), traced, func(s platform.Snapshot) error {
		for _, n := range []int{1, 51} {
			before := counter.Counts().Queries
			got, _, err := conversationpg.TopicUnreadIn(s).Count(t.Context(), f.OrganizationID, f.Channel.ID, 2, ids[:n], make([]int64, n), nil, &ids[n-1])
			feedRequire(t, err)
			if len(got) != n || counter.Counts().Queries-before != 1 {
				t.Fatalf("%d topics: counts=%v statements=%d", n, got, counter.Counts().Queries-before)
			}
			before = counter.Counts().Queries
			got, _, err = newTopicCounts().Read(t.Context(), s, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}, 1, ids[:n-1], &ids[n-1])
			feedRequire(t, err)
			if len(got) != n || counter.Counts().Queries-before != 2 {
				t.Fatalf("combined %d topics: counts=%v statements=%d", n, got, counter.Counts().Queries-before)
			}
		}
		return nil
	}))
}
