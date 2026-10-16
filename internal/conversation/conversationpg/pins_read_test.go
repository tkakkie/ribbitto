package conversationpg_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestReaderOverview(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	m, curator := membership(f.OrganizationID, f.MemberID), membership(f.OrganizationID, orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "curator@example.org", "Curator"), org.RoleMember, "curator", 1))
	m.Member.OrganizationID, curator.Member.OrganizationID = f.OrganizationID, f.OrganizationID
	first, err := newPosting(pool).Post(ctx, m, f.Channel.ID, "in\n\ndesign")
	requireNoError(t, err)
	second, err := newPosting(pool).Post(ctx, m, f.Channel.ID, "stays")
	requireNoError(t, err)
	_, err = conversationpg.NewBrancher(pool, eventSequence, appendEvents, branchReads, nil).Branch(ctx, m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{first.ID}, From: first.TopicID, NewName: "design"}, func(conversation.Topic) string { return "moved" })
	requireNoError(t, err)
	pinning := conversationpg.NewPinning(pool, eventSequence, appendEvents, nil)
	requireNoError(t, pinning.Pin(ctx, m, f.Channel.ID, first.ID))
	requireNoError(t, pinning.Pin(ctx, curator, f.Channel.ID, second.ID))
	readState := func() (n int) {
		requireNoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channel_read)+(SELECT count(*) FROM read_range)+(SELECT count(*) FROM topic_read_floor)`).Scan(&n))
		return n
	}
	before := readState()
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, emptyPageCountsIn)
	page, err := reader.Overview(ctx, m, f.Channel.ID, nil, nil, 0)
	requireNoError(t, err)
	// Every topic, newest pin first, with current topics and names; the
	// last event is the second pin, so the page cursor is the pins' snapshot.
	if len(page.Pins) != 2 || page.EventCursor == nil || *page.EventCursor != eventSeq(t, pool, f.OrganizationID) {
		t.Fatalf("page = %+v", page)
	}
	newest, oldest := page.Pins[0], page.Pins[1]
	if newest.ID != second.ID || !newest.DefaultTopic || newest.PinnerName != "Curator" || newest.PinnerHandle != "curator" || newest.Handle != "owner" {
		t.Fatalf("newest = %+v", newest)
	}
	if oldest.ID != first.ID || oldest.DefaultTopic || oldest.TopicName != "design" || oldest.Excerpt != "in design" || oldest.PinnerHandle != "owner" || !oldest.PinnedAt.Before(newest.PinnedAt) {
		t.Fatalf("oldest = %+v", oldest)
	}
	if readState() != before {
		t.Fatal("reading pins changed read state")
	}

	// Each scope alone: another organisation's member reading this channel,
	// and this member reading another organisation's channel.
	for name, tc := range map[string]struct {
		m       org.Membership
		channel kernel.ID
	}{
		"organisation": {membership(other.OrganizationID, other.MemberID), f.Channel.ID},
		"channel":      {m, other.Channel.ID},
	} {
		got, err := reader.Overview(ctx, tc.m, tc.channel, nil, nil, 0)
		if !errors.Is(err, conversation.ErrChannelNotFound) || got.Pins != nil {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
	}
}
