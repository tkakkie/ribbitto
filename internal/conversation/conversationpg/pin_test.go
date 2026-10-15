package conversationpg_test

import (
	"testing"

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

func TestPinsCurrentBranchAndDirectories(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	account := identitytest.Account(t, pool, "curator@example.org", "Pin curator")
	pinner := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "curator", 1)
	member := membership(f.OrganizationID, f.MemberID)
	m, err := newPosting(pool).Post(ctx, member, f.Channel.ID, "original text")
	requireNoError(t, err)
	var before conversation.PinState
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
		w := postgres.WriterIn(tx)
		requireNoError(t, w.SetMessagePin(ctx, f.OrganizationID, f.Channel.ID, m.ID, pinner))
		before, err = w.GetMessagePin(ctx, f.OrganizationID, f.Channel.ID, m.ID)
		return err
	}))
	// A fixture-only edit proves that pinning retained no frozen body.
	_, err = pool.Exec(ctx, "UPDATE message SET body = 'current text' WHERE id = $1", m.ID)
	requireNoError(t, err)
	brancher := conversationpg.NewBrancher(pool, eventSequence, appendEvents, branchReads, nil)
	dest, err := brancher.Branch(ctx, member, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{m.ID}, From: m.TopicID, NewName: "design"}, func(conversation.Topic) string { return "moved" })
	requireNoError(t, err)
	requireNoError(t, platform.InSnapshot(ctx, pool, func(s platform.Snapshot) error {
		r := postgres.ReadStoreIn(s)
		pins, err := r.ListChannelPins(ctx, f.OrganizationID, f.Channel.ID)
		requireNoError(t, err)
		if len(pins) != 1 || pins[0].ID != m.ID || pins[0].Body != "current text" || pins[0].TopicID != dest.ID || pins[0].MemberID != f.MemberID || pins[0].PinnedByMemberID != pinner || pins[0].PinState != before {
			t.Fatalf("current pin: %+v", pins)
		}
		topics, err := r.LookupTopics(ctx, f.OrganizationID, f.Channel.ID, []kernel.ID{pins[0].TopicID})
		requireNoError(t, err)
		members, err := lookupMembers(s).LookupMembers(ctx, f.OrganizationID, []kernel.ID{pins[0].MemberID, pins[0].PinnedByMemberID})
		requireNoError(t, err)
		names, err := lookupAccounts(s).LookupDisplayNames(ctx, []kernel.ID{members[pins[0].MemberID].AccountID, members[pinner].AccountID})
		requireNoError(t, err)
		if topics[dest.ID].Name != "design" || members[pins[0].MemberID].Handle != "owner" || names[members[pins[0].MemberID].AccountID] != "acme" || members[pinner].Handle != "curator" || names[members[pinner].AccountID] != "Pin curator" {
			t.Fatalf("directories: %v, %v, %v", topics, members, names)
		}
		requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
			return postgres.WriterIn(tx).ClearMessagePin(ctx, f.OrganizationID, f.Channel.ID, m.ID)
		}))
		again, err := r.ListChannelPins(ctx, f.OrganizationID, f.Channel.ID)
		if err != nil || len(again) != 1 || again[0] != pins[0] {
			t.Fatalf("snapshot changed: %+v, %v", again, err)
		}
		return nil
	}))
}
