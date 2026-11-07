package conversationpg_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestReplyPages(t *testing.T) {
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
				p := mentionPosting(pool)
				targets := make(map[kernel.ID]kernel.ID)
				for _, ch := range []kernel.ID{f.Channel.ID, second.ID, hidden.ID} {
					target, err := p.Post(t.Context(), viewer, ch, "answer me")
					requireNoError(t, err)
					targets[ch] = target.ID
				}
				foreignViewer := membership(foreign.OrganizationID, foreign.MemberID)
				target, err := p.Post(t.Context(), foreignViewer, foreign.Channel.ID, "foreign")
				requireNoError(t, err)
				foreignAuthor := orgtest.Member(t, pool, foreign.OrganizationID, f.AccountID, org.RoleMember, "visitor", 1)
				_, err = p.Send(t.Context(), membership(foreign.OrganizationID, foreignAuthor), foreign.Channel.ID, conversation.Send{Body: "foreign reply", ReplyTo: &target.ID})
				requireNoError(t, err)
				send := func(m org.Membership, ch kernel.ID) conversation.Message {
					t.Helper()
					sent, err := p.Send(t.Context(), m, ch, conversation.Send{Body: "answer", ReplyTo: new(targets[ch])})
					requireNoError(t, err)
					return sent.Message
				}
				var want []conversation.Message
				for i := range count + 1 {
					if filtered {
						send(bob, hidden.ID)
						send(viewer, f.Channel.ID)
					}
					if i < count {
						channel := []kernel.ID{f.Channel.ID, second.ID}[i%2]
						posted := send(bob, channel)
						want = append(want, posted)
					}
				}
				slices.Reverse(want)
				counter := platform.NewQueryCounter()
				config := pool.Config()
				config.ConnConfig.Tracer = counter
				traced, err := pgxpool.NewWithConfig(t.Context(), config)
				requireNoError(t, err)
				defer traced.Close()
				reader := conversationpg.NewReplies(traced)
				allowed := []kernel.ID{f.Channel.ID, second.ID, foreign.Channel.ID}
				if filtered {
					reader = conversation.NewReplies(conversationpg.NewSnapshotRunnerForTest(traced), func(s platform.Snapshot) conversation.ReplyReader {
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
				stateBefore := mentionState(t, pool, f.OrganizationID)
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
				if mentionState(t, pool, f.OrganizationID) != stateBefore {
					t.Fatal("reply reads changed durable state")
				}
				// Even a supplied foreign channel and its member cannot cross the org bound.
				for _, memberID := range []kernel.ID{foreign.MemberID, {}} {
					viewer.Member.ID = memberID
					for _, cursor := range []*int64{nil, new(int64(0)), new(int64(1 << 62))} {
						page, err := reader.Page(t.Context(), viewer, cursor)
						requireNoError(t, err)
						if len(page.Messages) != 0 || page.Next != nil {
							t.Fatalf("unauthorized member returned %+v", page)
						}
					}
				}
			})
		}
	}
}
