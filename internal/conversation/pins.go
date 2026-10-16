package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PinExcerptLength bounds a pin's excerpt in Unicode code points, ellipsis
// included (docs/domain/pins.md).
const PinExcerptLength = 200

// Pin is one overview entry: the current message with its author and topic,
// its excerpt, and who pinned it when, with current names.
type Pin struct {
	Entry
	Excerpt                  string
	PinnerName, PinnerHandle string
	PinnedAt                 time.Time
}

// Pins reads only the channel's bounded pins, current topics and names in
// one read-only snapshot. Like Members, public-channel access is checked
// against the resolved membership's organisation before reading the list.
func (s *Reader) Pins(ctx context.Context, m org.Membership, channelID kernel.ID) (pins []Pin, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		if _, err := reads.GetChannel(ctx, m.Organization.ID, channelID); err != nil {
			return err
		}
		pins, err = s.pins(ctx, snapshot, reads, m, channelID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading pins snapshot: %w", err)
	}
	return pins, nil
}

// Overview reads the page Page or Around would (around when it is positive,
// else before) and the channel's bounded pins across all its topics, with
// their topics and author and pinner names, in the page's snapshot, so the
// pins and the page cursor agree. Reading changes no read state.
func (s *Reader) Overview(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, before *int64, around int64) (ChannelPage, error) {
	return s.page(ctx, m, channelID, topicID, func(reader historyReader) (Page, error) {
		if around > 0 {
			return reader.Around(ctx, m, channelID, topicID, around)
		}
		return reader.Before(ctx, m, channelID, topicID, before)
	}, true)
}

// pins reads the channel's pins with their authors and topics, and the
// pinners in the same member batch: a bounded number of reads, never one
// per pin.
func (s *Reader) pins(ctx context.Context, snapshot platform.Snapshot, reads ReadStore, m org.Membership, channelID kernel.ID) ([]Pin, error) {
	pinned, err := reads.ListChannelPins(ctx, m.Organization.ID, channelID)
	if err != nil {
		return nil, err
	}
	topicIDs, memberIDs := make([]kernel.ID, len(pinned)), make([]kernel.ID, 0, 2*len(pinned))
	for i, p := range pinned {
		topicIDs[i], memberIDs = p.TopicID, append(memberIDs, p.MemberID, p.PinnedByMemberID)
	}
	topics, err := reads.LookupTopics(ctx, m.Organization.ID, channelID, topicIDs)
	if err != nil {
		return nil, fmt.Errorf("reading pin topics: %w", err)
	}
	members, err := s.members(snapshot).LookupMembers(ctx, m.Organization.ID, memberIDs)
	if err != nil {
		return nil, fmt.Errorf("reading pin members: %w", err)
	}
	accountIDs := make([]kernel.ID, 0, len(members))
	for _, member := range members {
		accountIDs = append(accountIDs, member.AccountID)
	}
	names, err := s.accounts(snapshot).LookupDisplayNames(ctx, accountIDs)
	if err != nil {
		return nil, fmt.Errorf("reading pin names: %w", err)
	}
	pins := make([]Pin, len(pinned))
	for i, p := range pinned {
		author, pinner, topic := members[p.MemberID], members[p.PinnedByMemberID], topics[p.TopicID]
		authorName, named := names[author.AccountID]
		pinnerName, pinnerNamed := names[pinner.AccountID]
		if !named || !pinnerNamed || topic.ID != p.TopicID {
			return nil, fmt.Errorf("missing names or topic for pin %x", p.ID)
		}
		pins[i] = Pin{
			Entry:   Entry{Message: p.Message, DisplayName: authorName, Handle: author.Handle, TopicName: topic.Name, DefaultTopic: topic.IsDefault},
			Excerpt: PinExcerpt(p.Body), PinnerName: pinnerName, PinnerHandle: pinner.Handle, PinnedAt: p.PinnedAt,
		}
	}
	return pins, nil
}

// PinExcerpt collapses whitespace runs to one space and shortens the result
// to PinExcerptLength code points, ending a shortened one with an ellipsis.
// It reads plain text only; a future body format needs its own extraction
// (decision 18).
func PinExcerpt(body string) string {
	text := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(text) <= PinExcerptLength {
		return text
	}
	runes := []rune(text)
	return strings.TrimRight(string(runes[:PinExcerptLength-1]), " ") + "…"
}
