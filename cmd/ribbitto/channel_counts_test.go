package main

import (
	"fmt"
	"testing"

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
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestChannelCounts(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		messages, topics, joined, want, first int64
	}{
		{"empty channel", 0, 1, 1, 0, 0},
		{"count 0 all read", 5, 1, 6, 0, 0},
		{"count 1", 1, 1, 1, 1, 2},
		{"count 99", 99, 1, 1, 99, 2},
		{"count 100 capped", 100, 1, 1, 100, 2},
		{"more than 100 unread", 1000, 1, 1, 100, 2},
		{"more than 50 topics", 51, 51, 1, 51, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "counts", "general")
			channel := conversationtest.Channel(t, pool, f.OrganizationID, tc.name, false)
			for i := int64(0); i < tc.topics; i++ {
				topic := channel.DefaultTopicID
				if i > 0 {
					topic = conversationtest.Topic(t, pool, f.OrganizationID, channel.ID, fmt.Sprint(i)).ID
				}
				_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,$3,$4,'unread',s+1 FROM generate_series(1,$5::bigint) s WHERE (s-1)%$6=$7`, f.OrganizationID, channel.ID, topic, f.MemberID, tc.messages, tc.topics, i)
				feedRequire(t, err)
			}
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, err := newChannelCounts().Read(t.Context(), s, f.OrganizationID, f.MemberID, tc.joined, []kernel.ID{channel.ID})
				feedRequire(t, err)
				if got[channel.ID] != (unread.ChannelCount{Count: tc.want, FirstUnread: tc.first}) {
					t.Fatalf("counts=%v", got)
				}
				return nil
			}))
		})
	}
}

func TestChannelCountsFirstGapBeforeCap(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "first-gap", "general")
	_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,$3,$4,'unread',s FROM generate_series(2,301) s`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	feedRequire(t, err)
	feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}, 1, unread.Range{Lo: 5, Hi: 41})
	}))
	// The three unread messages in the first gap must survive the cap even
	// though the later gap alone contains more than 100 unread messages.
	feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
		got, err := newChannelCounts().Read(t.Context(), s, f.OrganizationID, f.MemberID, 1, []kernel.ID{f.Channel.ID})
		feedRequire(t, err)
		if got[f.Channel.ID] != (unread.ChannelCount{Count: 100, FirstUnread: 2}) {
			t.Fatalf("counts=%v", got)
		}
		return nil
	}))
}

func TestChannelCountsStatementsAndDuplicate(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "statements", "general")
	ids := []kernel.ID{f.Channel.ID}
	for i := range 10 {
		channel := conversationtest.Channel(t, pool, f.OrganizationID, fmt.Sprint(i), false)
		ids = append(ids, channel.ID)
		_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,$3,$4,'unread',s+$6 FROM generate_series(2,$5::bigint) s`, f.OrganizationID, channel.ID, channel.DefaultTopicID, f.MemberID, i+3, i*20)
		feedRequire(t, err)
		feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel.ID, MemberID: f.MemberID}, 1, unread.Range{Lo: int64(i*20 + 3), Hi: int64(i*20 + 4)})
		}))
	}
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	feedRequire(t, err)
	defer traced.Close()
	feedRequire(t, platform.InSnapshot(t.Context(), traced, func(s platform.Snapshot) error {
		for _, tc := range []struct {
			name string
			ids  []kernel.ID
			want int
		}{
			{"one channel", ids[1:2], 1}, {"many channels", ids, len(ids)},
			{"duplicated sidebar ID", []kernel.ID{ids[1], ids[1]}, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := counter.Counts().Queries
				got, err := newChannelCounts().Read(t.Context(), s, f.OrganizationID, f.MemberID, 1, tc.ids)
				feedRequire(t, err)
				if len(got) != tc.want || counter.Counts().Queries-before != 2 {
					t.Fatalf("counts=%v, statements=%d", got, counter.Counts().Queries-before)
				}
				for i, id := range ids[1:] {
					if count, ok := got[id]; ok && count != (unread.ChannelCount{Count: int64(i + 1), FirstUnread: int64(i*20 + 2)}) {
						t.Fatalf("channel %d: %v", i, count)
					}
				}
			})
		}
		return nil
	}))
}

func TestChannelCountsScope(t *testing.T) {
	for _, dimension := range []string{"organization", "channel"} {
		t.Run(dimension, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
			organization, channel := f.OrganizationID, f.Channel.ID
			// Relax FKs only in this disposable clone to isolate each predicate.
			_, err := pool.Exec(t.Context(), `ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey,
 DROP CONSTRAINT message_topic_fkey, DROP CONSTRAINT message_organization_id_member_id_fkey`)
			feedRequire(t, err)
			if dimension == "organization" {
				organization = kernel.ID{1}
			} else {
				channel = kernel.ID{1}
			}
			_, err = pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 VALUES ($1,$2,$3,$4,'single-scope decoy',2)`, organization, channel, f.Channel.DefaultTopicID, f.MemberID)
			feedRequire(t, err)
			feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
				got, err := newChannelCounts().Read(t.Context(), s, f.OrganizationID, f.MemberID, 1, []kernel.ID{f.Channel.ID})
				feedRequire(t, err)
				if got[f.Channel.ID] != (unread.ChannelCount{}) {
					t.Fatalf("single-scope decoy leaked: %v", got)
				}
				return nil
			}))
		})
	}
}

func TestChannelCountsMovedMessagesAndBranchNotice(t *testing.T) {
	pool, scope := feedFixture(t) // messages 2–6
	account := identitytest.Account(t, pool, "counter@example.org", "Counter")
	scope.MemberID = orgtest.Member(t, pool, scope.OrganizationID, account, org.RoleMember, "counter", 1)
	feedRequire(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
		return unreadpg.WriterIn(tx).Merge(t.Context(), scope, 1, unread.Range{Lo: 0, Hi: 3})
	}))
	check := func(want int64) {
		feedRequire(t, platform.InSnapshot(t.Context(), pool, func(s platform.Snapshot) error {
			got, err := newChannelCounts().Read(t.Context(), s, scope.OrganizationID, scope.MemberID, 1, []kernel.ID{scope.ChannelID})
			feedRequire(t, err)
			if got[scope.ChannelID] != (unread.ChannelCount{Count: want, FirstUnread: 3}) {
				t.Fatalf("counts=%v", got)
			}
			return nil
		}))
	}
	check(4)
	var source, author kernel.ID
	var messages []kernel.ID
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&source))
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT member_id FROM message WHERE channel_id=$1 LIMIT 1", scope.ChannelID).Scan(&author))
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT array_agg(id ORDER BY event_seq) FROM message WHERE channel_id=$1 AND event_seq IN (2,6)", scope.ChannelID).Scan(&messages))
	brancher := conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, nil)
	_, err := brancher.Branch(t.Context(), org.Membership{Organization: org.Organization{ID: scope.OrganizationID}, Member: org.Member{ID: author, JoinedEventSeq: 1}}, scope.ChannelID,
		conversation.Branch{Messages: messages, From: source, NewName: "moved"}, func(conversation.Topic) string { return "notice" })
	feedRequire(t, err)
	check(5) // read 2 and unread 6 retain their contribution; notice 8 adds one.
}
