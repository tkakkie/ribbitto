package conversationpg_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

type readableChannelsFunc func(context.Context, org.Membership) ([]kernel.ID, error)

func (f readableChannelsFunc) ReadableChannelIDs(ctx context.Context, m org.Membership) ([]kernel.ID, error) {
	return f(ctx, m)
}

func TestMentionPages(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 50, 51, 101} {
		for _, filtered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/filtered=%t", count, filtered), func(t *testing.T) {
				pool := pgtest.New(t)
				f := mentionFixture(t, pool)
				viewer := membership(f.OrganizationID, f.MemberID)
				bob := membership(f.OrganizationID, orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob"), org.RoleMember, "bob", 1))
				second := conversationtest.Channel(t, pool, f.OrganizationID, "second", false)
				hidden := conversationtest.Channel(t, pool, f.OrganizationID, "hidden", false)
				foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", "general")
				_, err := pool.Exec(t.Context(), "UPDATE organization SET event_log_boundary_seq = event_seq WHERE id = $1", foreign.OrganizationID)
				requireNoError(t, err)
				// A foreign mention exists even when this organisation's page is empty.
				foreignAuthor := orgtest.Member(t, pool, foreign.OrganizationID, f.AccountID, org.RoleMember, "visitor", 1)
				_, err = mentionPosting(pool).Post(t.Context(), membership(foreign.OrganizationID, foreignAuthor), foreign.Channel.ID, mentionToken(foreign.MemberID))
				requireNoError(t, err)
				var want []conversation.Message
				for i := range count + 1 {
					if filtered {
						_, err = mentionPosting(pool).Post(t.Context(), bob, hidden.ID, mentionToken(f.MemberID))
						requireNoError(t, err)
						_, err = mentionPosting(pool).Post(t.Context(), viewer, f.Channel.ID, mentionToken(f.MemberID))
						requireNoError(t, err)
					}
					if i < count {
						channel := []kernel.ID{f.Channel.ID, second.ID}[i%2]
						posted, err := mentionPosting(pool).Post(t.Context(), bob, channel, mentionToken(f.MemberID))
						requireNoError(t, err)
						want = append(want, posted)
					}
				}
				if count == 101 && filtered {
					moved, err := conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, nil).Branch(t.Context(), viewer, f.Channel.ID,
						conversation.Branch{Messages: []kernel.ID{want[0].ID}, From: want[0].TopicID, NewName: "moved"}, func(conversation.Topic) string { return "moved" })
					requireNoError(t, err)
					want[0].TopicID = moved.ID
					conversationtest.AssertMentions(t, pool, f.OrganizationID, want[0].ID, want[0].EventSeq, []kernel.ID{f.MemberID})
				}
				slices.Reverse(want)
				counter := platform.NewQueryCounter()
				config := pool.Config()
				config.ConnConfig.Tracer = counter
				traced, err := pgxpool.NewWithConfig(t.Context(), config)
				requireNoError(t, err)
				defer traced.Close()
				reader := conversationpg.NewMentions(traced)
				allowed := []kernel.ID{f.Channel.ID, second.ID, foreign.Channel.ID}
				if filtered {
					reader = conversation.NewMentions(conversationpg.NewSnapshotRunnerForTest(traced), func(s platform.Snapshot) conversation.MentionReader {
						return postgres.ReadStoreIn(s)
					}, func(platform.Snapshot) conversation.ReadableChannels {
						return readableChannelsFunc(func(_ context.Context, m org.Membership) ([]kernel.ID, error) {
							if m != viewer {
								t.Fatal("read access received another membership")
							}
							return allowed, nil
						})
					})
				}
				var before *int64
				for offset := 0; ; offset += 50 {
					prior := counter.Counts()
					page, err := reader.Page(t.Context(), viewer, before)
					requireNoError(t, err)
					after := counter.Counts()
					queries := int64(2) // Production adds the channel listing to the single page statement.
					if filtered {
						queries = 1
					}
					if after.Queries-prior.Queries != queries || after.Begins-prior.Begins != 1 || after.Commits-prior.Commits != 1 {
						t.Fatalf("page statements: before=%+v after=%+v", prior, after)
					}
					end := min(offset+50, count)
					if !reflect.DeepEqual(page.Messages, append([]conversation.Message{}, want[offset:end]...)) {
						t.Fatalf("offset %d: page=%+v, want=%+v", offset, page.Messages, want[offset:end])
					}
					if (page.Next != nil) != (end < count) {
						t.Fatalf("offset %d: wrong next-page signal: %v", offset, page.Next)
					}
					if page.Next == nil {
						break
					}
					if *page.Next != want[end-1].EventSeq {
						t.Fatalf("cursor=%d, want last returned sequence %d", *page.Next, want[end-1].EventSeq)
					}
					before = page.Next
				}
				if filtered {
					allowed = nil
					page, err := reader.Page(t.Context(), viewer, nil)
					requireNoError(t, err)
					if len(page.Messages) != 0 || page.Next != nil {
						t.Fatalf("empty read set returned %+v", page)
					}
					allowed = []kernel.ID{foreign.Channel.ID}
				}
				// Even a supplied foreign channel and its member cannot cross the org bound.
				viewer.Member.ID = foreign.MemberID
				page, err := reader.Page(t.Context(), viewer, before)
				requireNoError(t, err)
				if len(page.Messages) != 0 || page.Next != nil {
					t.Fatalf("foreign member returned %+v", page)
				}
			})
		}
	}
}
