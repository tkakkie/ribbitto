package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type pageTrace struct {
	*platform.QueryCounter
	names map[string]int
	hook  func(string)
}

func (p *pageTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	p.QueryCounter.TraceQueryStart(ctx, conn, data)
	if fields := strings.Fields(data.SQL); len(fields) > 2 && fields[1] == "name:" {
		p.names[fields[2]]++
		p.hook(fields[2])
	}
	return ctx
}

func TestPageCountsSnapshot(t *testing.T) {
	statements := []string{"ListMessagesBefore", "GetEventSeq", "FirstChannelReadRanges", "CountChannelUnread", "ReadTopicState", "CountTopicUnread"}
	for _, n := range []int{1, 51} {
		for _, kind := range []string{"feed", "topic", "feed older", "topic older", "members"} {
			for _, statement := range statements {
				if kind == "members" && statement == "ListMessagesBefore" {
					continue
				}
				t.Run(fmt.Sprintf("%d/%s/%s", n, kind, statement), func(t *testing.T) {
					pool := pgtest.New(t)
					f := conversationtest.OrganizationWithOwner(t, pool, "page", "general")
					selected := f.Channel.DefaultTopicID
					for i := 1; i < n; i++ {
						conversationtest.Channel(t, pool, f.OrganizationID, fmt.Sprintf("channel%02d", i), false)
						selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
					}
					m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "page")
					feedRequire(t, err)
					// A pre-join message makes a missing join boundary observable.
					_, err = pool.Exec(t.Context(), "INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'pre-join',1)", f.OrganizationID, f.Channel.ID, selected, f.MemberID)
					feedRequire(t, err)
					posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
					initial, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "initial")
					feedRequire(t, err)
					_, err = posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "second")
					feedRequire(t, err)
					scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
					wantCount, wantFirst := int64(2), int64(2)
					if n > 1 {
						feedRequire(t, unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter()).Feed(t.Context(), scope, m.Member.JoinedEventSeq, initial.EventSeq))
						wantCount, wantFirst = 1, 3
					}
					trace := &pageTrace{QueryCounter: platform.NewQueryCounter(), names: map[string]int{}}
					committed := false
					trace.hook = func(name string) {
						if name != statement || committed {
							return
						}
						committed = true
						post, err := posting.PostToTopic(t.Context(), m, f.Channel.ID, &selected, "concurrent")
						feedRequire(t, err)
						scope := unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
						feedRequire(t, unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter()).Feed(t.Context(), scope, m.Member.JoinedEventSeq, post.EventSeq))
						_, err = conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, nil).Branch(t.Context(), m, f.Channel.ID,
							conversation.Branch{From: selected, Messages: []kernel.ID{initial.ID}, NewName: "moved"}, func(conversation.Topic) string { return "notice" })
						feedRequire(t, err)
					}
					config := pool.Config()
					config.ConnConfig.Tracer = trace
					traced, err := pgxpool.NewWithConfig(t.Context(), config)
					feedRequire(t, err)
					defer traced.Close()
					reader := conversationpg.NewReader(traced, lookupMembers, lookupAccounts, eventCursor, pageCountsIn)
					var counts conversation.PageCounts
					var cursor *int64
					wantTopics := min(n, 50)
					if kind == "members" {
						page, err := reader.Members(t.Context(), m, f.Channel.ID, nil)
						feedRequire(t, err)
						counts, cursor = page.PageCounts, page.EventCursor
					} else {
						var before *int64
						if strings.Contains(kind, "older") {
							bound := int64(3)
							before = &bound
						}
						var topic *kernel.ID
						if strings.HasPrefix(kind, "topic") {
							topic = &selected
							wantTopics = n
						}
						page, err := reader.Page(t.Context(), m, f.Channel.ID, topic, before)
						feedRequire(t, err)
						counts, cursor = page.PageCounts, page.EventCursor
						wantHistory := 3
						if before != nil {
							wantHistory = 2
						}
						if len(page.Entries) != wantHistory || page.Entries[1].ID != initial.ID || page.Entries[1].TopicID != selected {
							t.Fatalf("history mixed snapshots: %+v", page.Entries)
						}
					}
					first := int64(0)
					if strings.HasPrefix(kind, "topic") {
						first = wantFirst
					}
					if !committed || cursor == nil || *cursor != 3 || len(counts.ChannelCounts) != n || len(counts.TopicCounts) != wantTopics ||
						counts.ChannelCounts[f.Channel.ID] != wantCount || counts.FeedFirstUnread != wantFirst || counts.TopicFirstUnread != first {
						t.Fatalf("counts/cursor mixed snapshots: %+v cursor=%v", counts, cursor)
					}
					if strings.HasPrefix(kind, "topic") && counts.TopicCounts[selected] != wantCount {
						t.Fatal("selected topic missing or counted twice")
					}
					for _, name := range statements[2:] {
						if trace.names[name] != 1 {
							t.Fatalf("%s statements=%d, want 1", name, trace.names[name])
						}
					}
					wantStatements := int64(12)
					if strings.HasPrefix(kind, "topic") {
						wantStatements = 13
					}
					if kind == "members" {
						wantStatements = 10
					}
					if q := trace.Counts(); q.Begins != 1 || q.Commits != 1 || q.Rollbacks != 0 || q.Queries != wantStatements {
						t.Fatalf("page transactions: %+v", q)
					}
					fresh, err := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn).Page(t.Context(), m, f.Channel.ID, &selected, nil)
					feedRequire(t, err)
					if fresh.ChannelCounts[f.Channel.ID] != 0 || *fresh.EventCursor != 6 {
						t.Fatalf("concurrent changes did not commit: %+v", fresh)
					}
				})
			}
		}
	}
}
