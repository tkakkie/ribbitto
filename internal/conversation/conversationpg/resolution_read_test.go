package conversationpg_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestResolvedTopicSidebarSelection(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "resolution", "general")
	m := membership(f.OrganizationID, f.MemberID)
	m.Member.OrganizationID, m.Member.JoinedEventSeq = f.OrganizationID, 1
	var want []kernel.ID
	want = append(want, f.Channel.DefaultTopicID)
	for i := range 50 {
		topic := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("open-%02d", i))
		want = append(want, topic.ID)
	}
	// Topics of another channel and of another organisation sort first by name
	// and must never appear in this channel's list.
	sibling := conversationtest.Channel(t, pool, f.OrganizationID, "random", false)
	conversationtest.Topic(t, pool, f.OrganizationID, sibling.ID, "Aaa-other-channel")
	other := conversationtest.OrganizationWithOwner(t, pool, "resolution-other", "general")
	conversationtest.Topic(t, pool, other.OrganizationID, other.Channel.ID, "Aaa-other-organisation")
	// An earlier resolved name must still sort after every open topic.
	first := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "Alpha")
	target := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "Zulu")
	want = append(want, first.ID, target.ID)
	posting := newPosting(pool)
	conclusions := make(map[kernel.ID]kernel.ID)
	var firstUnread int64
	for _, topic := range []kernel.ID{first.ID, target.ID} {
		message, err := posting.PostToTopic(ctx, m, f.Channel.ID, &topic, "conclusion")
		requireNoError(t, err)
		// Fixture-only resolution: this checkpoint exposes reads, not the mutation API.
		_, err = pool.Exec(ctx, `UPDATE topic SET conclusion_message_id=$4 WHERE organization_id=$1 AND channel_id=$2 AND id=$3`, f.OrganizationID, f.Channel.ID, topic, message.ID)
		requireNoError(t, err)
		conclusions[topic] = message.ID
		firstUnread = message.EventSeq
	}
	_, err := posting.PostToTopic(ctx, m, f.Channel.ID, &target.ID, "a later reply")
	requireNoError(t, err)
	requireNoError(t, platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
		store := postgres.ReadStoreIn(snapshot)
		all, err := store.ListTopics(ctx, f.OrganizationID, f.Channel.ID, 100)
		requireNoError(t, err)
		var ids []kernel.ID
		for _, topic := range all {
			ids = append(ids, topic.ID)
			if topic.ConclusionMessageID != conclusions[topic.ID] {
				t.Fatalf("topic %s conclusion = %s, want %s", topic.Name, topic.ConclusionMessageID, conclusions[topic.ID])
			}
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("topic order = %v, want %v", ids, want)
		}
		batch, err := store.LookupTopics(ctx, f.OrganizationID, f.Channel.ID, []kernel.ID{target.ID})
		requireNoError(t, err)
		if batch[target.ID].ConclusionMessageID != conclusions[target.ID] {
			t.Fatal("batch lookup lost the conclusion")
		}
		return nil
	}))
	reader := conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn)
	sidebar, err := reader.Sidebar(ctx, m, f.Channel.ID, &target.ID)
	requireNoError(t, err)
	if len(sidebar.Topics) != 50 || slices.ContainsFunc(sidebar.Topics, func(topic conversation.Topic) bool { return topic.ID == target.ID }) {
		t.Fatal("resolved target must be beyond the bounded list")
	}
	if sidebar.Topic == nil || sidebar.Topic.ID != target.ID || sidebar.Topic.ConclusionMessageID != conclusions[target.ID] || sidebar.TopicCounts[target.ID] != 2 {
		t.Fatalf("selected topic or unread count lost: %+v, %v", sidebar.Topic, sidebar.TopicCounts)
	}
	page, err := reader.Page(ctx, m, f.Channel.ID, &target.ID, nil)
	requireNoError(t, err)
	if page.Topic == nil || page.Topic.ConclusionMessageID != conclusions[target.ID] || page.TopicCounts[target.ID] != 2 || page.TopicFirstUnread != firstUnread {
		t.Fatalf("selected page lost resolution or unread position: %+v", page)
	}
}
