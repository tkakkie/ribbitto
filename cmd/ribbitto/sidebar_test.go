package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestSidebarSnapshot(t *testing.T) {
	statements := []string{"ListChannels", "ListTopics", "FirstChannelReadRanges", "CountChannelUnread", "ReadTopicState", "CountTopicUnread"}
	for _, selectedView := range []bool{false, true} {
		for _, statement := range statements {
			t.Run(fmt.Sprintf("selected=%t/%s", selectedView, statement), func(t *testing.T) {
				pool := pgtest.New(t)
				f := conversationtest.OrganizationWithOwner(t, pool, "sidebar", "general")
				empty := conversationtest.Channel(t, pool, f.OrganizationID, "aaa", false)
				var selected kernel.ID
				for i := 1; i <= 50; i++ {
					selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
				}
				m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "sidebar")
				feedRequire(t, err)
				_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'pre-join',1)", f.OrganizationID, f.Channel.ID, selected, f.MemberID)
				feedRequire(t, err)
				posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
				initial, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "initial")
				feedRequire(t, err)
				_, err = posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "second")
				feedRequire(t, err)
				var topic *kernel.ID
				if selectedView {
					topic = &selected
				}
				reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn)
				page, err := reader.Page(t.Context(), m, f.Channel.ID, topic, nil)
				feedRequire(t, err)
				displaced := page.Topics[49].ID
				trace := &pageTrace{QueryCounter: platform.NewQueryCounter(), names: map[string]int{}}
				committed := false
				var moved conversation.Topic
				var added conversationtest.ChannelFixture
				trace.hook = func(name string) {
					if name != statement || committed {
						return
					}
					committed = true
					post, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "concurrent")
					feedRequire(t, err)
					scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
					feedRequire(t, unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), nil).Feed(t.Context(), scope, m.Member.JoinedEventSeq, post.EventSeq))
					branch, err := conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, nil).Branch(t.Context(), m, f.Channel.ID,
						conversation.Branch{From: selected, Messages: []kernel.ID{initial.ID}, NewName: "aaa"}, func(conversation.Topic) string { return "notice" })
					feedRequire(t, err)
					moved = branch
					added = conversationtest.Channel(t, pool, f.OrganizationID, "new", false)
				}
				config := pool.Config()
				config.ConnConfig.Tracer = trace
				traced, err := pgxpool.NewWithConfig(t.Context(), config)
				feedRequire(t, err)
				defer traced.Close()
				// Nil factories also prove this read cannot load authors or a cursor.
				sidebar, err := conversationpg.NewReader(traced, nil, nil, nil, pageCountsIn).Sidebar(t.Context(), m, f.Channel.ID, topic)
				feedRequire(t, err)
				if !committed || !reflect.DeepEqual(sidebar.Channels, page.Channels) || !reflect.DeepEqual(sidebar.Topics, page.Topics) ||
					!reflect.DeepEqual(sidebar.ChannelCounts, page.ChannelCounts) || !reflect.DeepEqual(sidebar.TopicCounts, page.TopicCounts) ||
					sidebar.ChannelCounts[f.Channel.ID] != 2 || sidebar.ChannelCounts[empty.ID] != 0 {
					t.Fatalf("sidebar mixed snapshots or disagrees with page: %+v", sidebar)
				}
				wantStatements := int64(7)
				if selectedView {
					wantStatements++
					if sidebar.Topic == nil || sidebar.Topic.ID != selected || sidebar.TopicCounts[selected] != 2 || len(sidebar.TopicCounts) != 51 {
						t.Fatal("unlisted selection missing or changed the list bound")
					}
				}
				if q := trace.Counts(); q.Begins != 1 || q.Commits != 1 || q.Rollbacks != 0 || q.Queries != wantStatements {
					t.Fatalf("sidebar transactions: %+v", q)
				}
				for _, name := range statements {
					if trace.names[name] != 1 {
						t.Fatalf("%s statements=%d, want 1", name, trace.names[name])
					}
				}
				fresh, err := reader.Sidebar(t.Context(), m, f.Channel.ID, topic)
				feedRequire(t, err)
				if len(fresh.Channels) != 3 || fresh.Channels[2].ID != added.ID || len(fresh.Topics) != 50 || fresh.Topics[1].ID != moved.ID {
					t.Fatalf("fresh lists or order incorrect: %+v", fresh)
				}
				if _, ok := fresh.TopicCounts[displaced]; ok {
					t.Fatal("displaced non-selected topic retained")
				}
				for _, channel := range fresh.Channels {
					if count, ok := fresh.ChannelCounts[channel.ID]; !ok || count != 0 {
						t.Fatal("fresh channel zero missing")
					}
				}
				for _, listed := range fresh.Topics {
					if count, ok := fresh.TopicCounts[listed.ID]; !ok || count != 0 {
						t.Fatal("fresh topic zero missing")
					}
				}
				if selectedView {
					if count, ok := fresh.TopicCounts[selected]; !ok || count != 0 {
						t.Fatal("selected topic zero missing")
					}
				}
			})
		}
	}
}

func TestSidebarScopes(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "sidebar", "general")
	m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "sidebar")
	feedRequire(t, err)
	reader := conversationpg.NewReader(pool, nil, nil, nil, pageCountsIn)
	posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	otherTopic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "other")
	post, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, nil, "unread")
	feedRequire(t, err)

	t.Run("organisation", func(t *testing.T) {
		foreign := orgtest.Organization(t, pool, "foreign", "foreign", 1)
		channel := conversationtest.Channel(t, pool, foreign, "foreign", true)
		sidebar, err := reader.Sidebar(t.Context(), m, f.Channel.ID, nil)
		feedRequire(t, err)
		if len(sidebar.Channels) != 1 {
			t.Fatal("foreign organisation channel leaked")
		}
		if _, err := reader.Sidebar(t.Context(), m, channel.ID, nil); !errors.Is(err, conversation.ErrChannelNotFound) {
			t.Fatalf("foreign channel error = %v", err)
		}
	})
	t.Run("member", func(t *testing.T) {
		account := identitytest.Account(t, pool, "other@example.org", "other")
		member := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "other", m.Member.JoinedEventSeq)
		scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: member}
		feedRequire(t, unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), nil).Feed(t.Context(), scope, m.Member.JoinedEventSeq, post.EventSeq))
		sidebar, err := reader.Sidebar(t.Context(), m, f.Channel.ID, nil)
		feedRequire(t, err)
		if sidebar.ChannelCounts[f.Channel.ID] != 1 || sidebar.TopicCounts[f.Channel.DefaultTopicID] != 1 {
			t.Fatal("other member read state leaked")
		}
	})
	t.Run("channel", func(t *testing.T) {
		channel := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
		sidebar, err := reader.Sidebar(t.Context(), m, f.Channel.ID, nil)
		feedRequire(t, err)
		if len(sidebar.Topics) != 2 {
			t.Fatal("other channel topic leaked")
		}
		if _, err := reader.Sidebar(t.Context(), m, f.Channel.ID, &channel.DefaultTopicID); !errors.Is(err, conversation.ErrTopicNotFound) {
			t.Fatalf("other channel selection error = %v", err)
		}
	})
	t.Run("topic", func(t *testing.T) {
		sidebar, err := reader.Sidebar(t.Context(), m, f.Channel.ID, &otherTopic.ID)
		feedRequire(t, err)
		if count, ok := sidebar.TopicCounts[otherTopic.ID]; !ok || count != 0 || sidebar.TopicCounts[f.Channel.DefaultTopicID] != 1 {
			t.Fatal("other topic message leaked")
		}
		if _, err := reader.Sidebar(t.Context(), m, f.Channel.ID, &kernel.ID{255}); !errors.Is(err, conversation.ErrTopicNotFound) {
			t.Fatalf("missing selection error = %v", err)
		}
	})
}
