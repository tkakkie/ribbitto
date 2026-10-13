package realtime_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// Branch returns only the destination. Capture the notice sequence from the
// production writer's own-message bounds, without querying post-write truth.
type counterBranchReads struct {
	tx     platform.Tx
	notice *int64
}

func (w counterBranchReads) Merge(ctx context.Context, organization, channel, member kernel.ID, joined, lo, hi int64) error {
	if err := unreadpg.WriterIn(w.tx).Merge(ctx, unread.Scope{OrganizationID: organization, ChannelID: channel, MemberID: member}, joined, unread.Range{Lo: lo, Hi: hi}); err != nil {
		return err
	}
	*w.notice = hi - 1
	return nil
}

func (h *counterMatrix) branch(t *testing.T, name string, m org.Membership, from kernel.ID, to *kernel.ID, newName string, sequences ...int64) (kernel.ID, int64) {
	t.Helper()
	ids := make([]kernel.ID, len(sequences))
	for i, seq := range sequences {
		ids[i] = h.posted[seq]
	}
	var notice int64
	brancher := conversationpg.NewBrancher(h.pool, postingSequence, postingEvents,
		func(tx platform.Tx) conversation.ReadRangeWriter { return counterBranchReads{tx, &notice} }, nil)
	destination, err := brancher.Branch(t.Context(), m, h.channel,
		conversation.Branch{Messages: ids, From: from, To: to, NewName: newName},
		func(conversation.Topic) string { return "branch notice" })
	if err != nil {
		t.Fatal(err)
	}
	h.prototype.move(h.truth, h.channel, from, destination.ID, sequences...)
	h.prototype.post(h.truth, counterMessage{channel: h.channel, topic: from, seq: notice}, m.Member.ID, kernel.ID{}, 0)
	h.selected = destination.ID
	h.check(t, name)
	return destination.ID, notice - 1 // Production allocates move immediately before notice.
}

func TestUnreadCountersMoves(t *testing.T) {
	h := newCounterMatrix(t)
	owner := h.members[0]
	writer := h.join(t, "writer") // Own messages are read, but no bundle exists.
	reader := h.join(t, "reader")
	h.prototype.materialize(h.truth, counterKey{reader.Member.ID, h.channel})
	for i := 0; i < 49; i++ {
		conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, fmt.Sprintf("Listed%02d", i))
	}
	destination := conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "ZDestination").ID
	unlisted := conversationtest.Topic(t, h.pool, owner.Organization.ID, h.channel, "ZOther").ID
	h.selected = destination
	other := conversationtest.Channel(t, h.pool, owner.Organization.ID, "sentinel", false)
	// Foreign organisation/members are metadata only; all messages and reads
	// below use production APIs. The same account also has a foreign membership.
	foreign := conversationtest.OrganizationWithOwner(t, h.pool, "foreign", "general")
	foreignOwner := org.Membership{Organization: org.Organization{ID: foreign.OrganizationID}, Member: org.Member{ID: foreign.MemberID, JoinedEventSeq: 1}}
	foreignWriter := foreignOwner
	foreignWriter.Member.ID = orgtest.Member(t, h.pool, foreign.OrganizationID, owner.Member.AccountID, org.RoleMember, "writer", 1)
	foreignWriter.Member.AccountID = owner.Member.AccountID
	h.extra = []counterView{{foreignOwner, foreign.Channel.ID, foreign.Channel.DefaultTopicID}}
	h.prototype.totals[other.ID] = map[kernel.ID]int64{}
	h.prototype.totals[foreign.Channel.ID] = map[kernel.ID]int64{}
	h.check(t, "metadata-and-unlisted-selection")
	h.prototype.materialize(h.truth, counterKey{owner.Member.ID, other.ID})
	h.prototype.materialize(h.truthByOrg[foreign.OrganizationID], counterKey{foreign.MemberID, foreign.Channel.ID})
	h.postIn(t, "other-channel-unread-sentinel", writer, other.ID, nil, nil)
	h.postIn(t, "foreign-unread-sentinel", foreignWriter, foreign.Channel.ID, nil, nil)
	h.post(t, "unlisted-sibling-contributes-to-channel", writer, &unlisted, nil)
	var sequences []int64
	for i := 0; i < 103; i++ {
		sequences = append(sequences, h.post(t, fmt.Sprintf("move-source-%d", i), writer, nil, nil))
	}
	h.read(t, "owner-reads-one-source-message", owner, &h.base, sequences[0])
	h.read(t, "reader-reads-two-source-messages", reader, &h.base, sequences[1])
	floor := sequences[102]
	h.read(t, "empty-destination-floor-above-moved-messages", owner, &destination, floor)
	h.floor(t, owner, destination, floor)
	_, move := h.branch(t, "existing-topic-mixed-read-transfer-below-floor", owner, h.base, &destination, "", sequences[:3]...)
	h.expect(t, owner, 2)
	h.expect(t, reader, 1)
	h.expect(t, writer, 0)
	h.floor(t, owner, destination, floor)
	h.read(t, "tab-A-destination-before-move-cursor", owner, &destination, move-1)
	h.expect(t, owner, 2)
	h.read(t, "tab-B-destination-at-move-cursor", owner, &destination, move)
	h.expect(t, owner, 0)
	h.read(t, "tab-A-reordered-pre-move-read", owner, &destination, floor)
	h.expect(t, owner, 0)
	h.postIn(t, "foreign-post-leaves-local-move-counts-alone", foreignWriter, foreign.Channel.ID, nil, nil)
	branched, nextMove := h.branch(t, "new-unlisted-topic-and-own-notice", reader, h.base, nil, "ZBranch", sequences[3:]...)
	h.expect(t, owner, 100)
	h.expect(t, reader, 100) // Branching reads only the notice, not the moved messages.
	h.expect(t, writer, 0)
	model := h.models[owner.Member.ID]
	if len(model.topics) != 50 {
		t.Fatal("sidebar must stay bounded with unlisted topics")
	}
	for _, entry := range model.topics {
		if entry.id == branched || entry.id == destination || entry.id == unlisted {
			t.Fatal("move destinations and sibling must remain unlisted")
		}
	}
	h.read(t, "tab-C-new-topic-before-branch-cursor", reader, &branched, nextMove-1)
	h.expect(t, reader, 100)
	h.read(t, "tab-D-new-topic-after-branch-cursor", reader, &branched, nextMove+1)
	h.expect(t, reader, 0)
	h.expect(t, owner, 100) // Another member's read cannot change this bundle.
	_, lastMove := h.branch(t, "repeat-move-with-missing-bundle-author", writer, branched, &destination, "", sequences[3])
	h.expect(t, owner, 1)
	h.expect(t, reader, 1) // The original unread destination message is untouched.
	h.expect(t, writer, 0)
	h.read(t, "tab-B-destination-before-latest-move", owner, &destination, nextMove)
	h.expect(t, owner, 1)
	h.read(t, "tab-B-destination-after-latest-move", owner, &destination, lastMove+1)
	h.expect(t, owner, 0)
	if h.prototype.bundles[counterKey{writer.Member.ID, h.channel}] != nil {
		t.Fatal("move author must retain missing-bundle fallback")
	}
}
