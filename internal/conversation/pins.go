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

// Pin is one overview entry: the current message with its author, topic
// and resolved references, and who pinned it when, with current names. Its
// excerpt is PinExcerpt's, with the caller's placeholder.
type Pin struct {
	Entry
	PinnerName, PinnerHandle string
	PinnedAt                 time.Time
}

// PinsSnapshot holds current pins and their snapshot sequence for ordering UI swaps.
type PinsSnapshot struct {
	Pins        []Pin
	SnapshotSeq int64
}

// Pins reads the bounded current pins without exposing their display sequence.
func (s *Reader) Pins(ctx context.Context, m org.Membership, channelID kernel.ID) ([]Pin, error) {
	state, err := s.ReadPins(ctx, m, channelID)
	return state.Pins, err
}

// ReadPins reads only the channel's bounded pins, current topics and names in
// one read-only snapshot. Like Members, public-channel access is checked
// against the resolved membership's organisation before reading the list.
func (s *Reader) ReadPins(ctx context.Context, m org.Membership, channelID kernel.ID) (state PinsSnapshot, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		if _, err := reads.GetChannel(ctx, m.Organization.ID, channelID); err != nil {
			return err
		}
		state.Pins, err = s.pins(ctx, snapshot, reads, m, channelID)
		if err != nil {
			return err
		}
		state.SnapshotSeq = int64(len(state.Pins))
		return nil
	})
	if err != nil {
		return PinsSnapshot{}, fmt.Errorf("reading pins snapshot: %w", err)
	}
	return state, nil
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
// pinners and chat_v1 bodies' references in the same organisation-scoped
// member batch: a bounded number of reads, never one per pin.
func (s *Reader) pins(ctx context.Context, snapshot platform.Snapshot, reads ReadStore, m org.Membership, channelID kernel.ID) ([]Pin, error) {
	pinned, err := reads.ListChannelPins(ctx, m.Organization.ID, channelID)
	if err != nil {
		return nil, err
	}
	topicIDs, memberIDs := make([]kernel.ID, len(pinned)), make([]kernel.ID, 0, 2*len(pinned))
	bodies := make([][]BodyBlock, len(pinned))
	for i, p := range pinned {
		topicIDs[i], memberIDs = p.TopicID, append(memberIDs, p.MemberID, p.PinnedByMemberID)
		if p.Format == FormatChatV1 {
			bodies[i] = ParseChatV1(p.Body)
			memberIDs = appendReferences(memberIDs, bodies[i])
		}
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
			Entry:      Entry{Message: p.Message, DisplayName: authorName, Handle: author.Handle, TopicName: topic.Name, DefaultTopic: topic.IsDefault},
			PinnerName: pinnerName, PinnerHandle: pinner.Handle, PinnedAt: p.PinnedAt,
		}
		if bodies[i] != nil {
			pins[i].Blocks, pins[i].References = bodies[i], resolveReferences(bodies[i], members, names)
		}
	}
	return pins, nil
}

// PinExcerpt collapses whitespace runs in the message's text (Entry.Text,
// with placeholder for an unresolved reference) to one space and shortens
// the result to PinExcerptLength code points, ending a shortened one with
// an ellipsis. It counts after resolution, so a chat_v1 excerpt never hides
// markup or a UUID within its length.
func PinExcerpt(entry Entry, placeholder string) string {
	text := strings.Join(strings.Fields(entry.Text(placeholder)), " ")
	if utf8.RuneCountInString(text) <= PinExcerptLength {
		return text
	}
	runes := []rune(text)
	return strings.TrimRight(string(runes[:PinExcerptLength-1]), " ") + "…"
}
