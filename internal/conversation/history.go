package conversation

import (
	"context"
	"fmt"
	"slices"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Entry is a stored message with its current author names and topic label.
type Entry struct {
	Message
	DisplayName, Handle string
	TopicName           string
	DefaultTopic        bool
	// Blocks is a chat_v1 body, parsed; it is nil for plain_text.
	Blocks []BodyBlock
	// References holds the members Blocks names that the organisation's
	// directory resolved. A reference missing from it is shown as unresolved.
	References map[kernel.ID]Reference
	// QuoteAuthor is the quoted member's current names when Quote is set.
	QuoteAuthor Reference
}

// Reference is a referenced member's current names.
type Reference struct {
	DisplayName, Handle string
}

// historyReader composes history with the topic labels and with the author lookups
// from org and identity.
type historyReader struct {
	Reads    ReadStore
	Members  MemberDirectory
	Accounts AccountDirectory
}

// PageSize is how many messages one page of history holds.
const PageSize = 50

// sidebarTopics is the most topics the channel sidebar lists, a domain rule
// (docs/domain/topics.md).
const sidebarTopics = 50

// Page is one page of a channel's history, oldest first.
type Page struct {
	Entries []Entry
	// Older reports that messages before Entries[0] exist; the next page is
	// read before Entries[0].EventSeq.
	Older bool
}

// Location is where a message is now. Latest reports that it is among its
// topic's newest PageSize messages, the topic's latest page.
type Location struct {
	ChannelID, TopicID kernel.ID
	EventSeq           int64
	Latest             bool
}

// ChannelPage is a channel, its sidebar and history read in one snapshot.
type ChannelPage struct {
	PageCounts
	Page
	Topic    *Topic
	Topics   []Topic
	Current  Channel
	Channels []Channel
	// Pins are the channel's pins, newest first; only Overview reads them.
	Pins []Pin
	// EventCursor is the snapshot's organisation sequence, including on older pages.
	EventCursor *int64
}

// Before returns the page of messages older than event_seq before, or the
// latest page when before is nil. A nil topicID includes every topic.
// The caller resolves membership and the
// channel through org.Authorizer and ReadStore before reading. before is only an upper
// bound: the query is scoped to the membership's organisation and the
// channel, so a value taken from another channel cannot reach its messages.
func (s historyReader) Before(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, before *int64) (Page, error) {
	// One extra row says whether an older page exists without a count query.
	messages, err := s.Reads.ListMessagesBefore(ctx, m.Organization.ID, channelID, topicID, before, PageSize+1)
	if err != nil {
		return Page{}, fmt.Errorf("reading history: %w", err)
	}
	older := len(messages) > PageSize
	if older {
		messages = messages[:PageSize]
	}
	entries, err := s.entries(ctx, m, channelID, messages)
	if err != nil {
		return Page{}, err
	}
	return Page{Entries: entries, Older: older}, nil
}

// Around returns the page holding event_seq around with context on both
// sides: up to half a page from around on, the rest older. Like Before it is
// bounded and scoped to the organisation, channel and topic, so a value taken
// from another channel or topic cannot reach its messages.
func (s historyReader) Around(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, around int64) (Page, error) {
	newer, err := s.Reads.ListMessagesAfter(ctx, m.Organization.ID, channelID, topicID, around-1, PageSize/2)
	if err != nil {
		return Page{}, fmt.Errorf("reading history around a message: %w", err)
	}
	room := PageSize - len(newer)
	older, err := s.Reads.ListMessagesBefore(ctx, m.Organization.ID, channelID, topicID, &around, int32(room+1))
	if err != nil {
		return Page{}, fmt.Errorf("reading history before a message: %w", err)
	}
	more := len(older) > room
	if more {
		older = older[:room]
	}
	// entries takes messages newest first.
	slices.Reverse(newer)
	entries, err := s.entries(ctx, m, channelID, append(newer, older...))
	if err != nil {
		return Page{}, err
	}
	return Page{Entries: entries, Older: more}, nil
}

// One returns a message with current author names, or ErrMessageNotFound.
// The caller resolves membership and channel access before reading, as for
// Before.
func (s historyReader) One(ctx context.Context, m org.Membership, channelID kernel.ID, eventSeq int64) (Entry, error) {
	msg, err := s.Reads.GetMessage(ctx, m.Organization.ID, channelID, eventSeq)
	if err != nil {
		return Entry{}, fmt.Errorf("reading message: %w", err)
	}
	entries, err := s.entries(ctx, m, channelID, []Message{msg})
	if err != nil {
		return Entry{}, err
	}
	return entries[0], nil
}

// Many reads exactly the requested messages with current labels and authors,
// oldest first. The ID list bounds the read; an incomplete batch fails replay.
func (s historyReader) Many(ctx context.Context, m org.Membership, channelID kernel.ID, ids []kernel.ID) ([]Entry, error) {
	messages, err := s.Reads.GetMessages(ctx, m.Organization.ID, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	if len(messages) != len(ids) {
		return nil, ErrMessageNotFound
	}
	return s.entries(ctx, m, channelID, messages)
}

// entries adds topic labels, author names, quoted authors and chat_v1 bodies
// with their references, returning messages oldest first. Quoted and
// referenced members join the authors' batches, so a page or a live render
// reads names once; they are
// looked up only in m's organisation, which omits foreign and missing
// members, so a stored reference cannot show another organisation's member.
func (s historyReader) entries(ctx context.Context, m org.Membership, channelID kernel.ID, messages []Message) ([]Entry, error) {
	ids := make([]kernel.ID, 0, len(messages))
	bodies := make([][]BodyBlock, len(messages))
	for i, msg := range messages {
		ids = append(ids, msg.MemberID)
		if msg.Quote != nil {
			ids = append(ids, msg.Quote.MemberID)
		}
		if msg.Format == FormatChatV1 {
			bodies[i] = ParseChatV1(msg.Body)
			ids = appendReferences(ids, bodies[i])
		}
	}
	members, err := s.Members.LookupMembers(ctx, m.Organization.ID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading authors: %w", err)
	}
	ids = ids[:0]
	for _, author := range members {
		ids = append(ids, author.AccountID)
	}
	names, err := s.Accounts.LookupDisplayNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading author names: %w", err)
	}
	ids = ids[:0]
	for _, msg := range messages {
		ids = append(ids, msg.TopicID)
	}
	topics, err := s.Reads.LookupTopics(ctx, m.Organization.ID, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading topics: %w", err)
	}
	entries := make([]Entry, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		author, ok := members[msg.MemberID]
		name, named := names[author.AccountID]
		if !ok || !named {
			return nil, fmt.Errorf("missing author for message %x", msg.ID)
		}
		topic, ok := topics[msg.TopicID]
		if !ok {
			return nil, fmt.Errorf("missing topic for message %x", msg.ID)
		}
		entry := Entry{Message: msg, DisplayName: name, Handle: author.Handle, TopicName: topic.Name, DefaultTopic: topic.IsDefault}
		if msg.Quote != nil {
			quoted, ok := members[msg.Quote.MemberID]
			if !ok {
				return nil, fmt.Errorf("missing quoted author for message %x", msg.ID)
			}
			entry.QuoteAuthor = Reference{DisplayName: names[quoted.AccountID], Handle: quoted.Handle}
		}
		if bodies[i] != nil {
			entry.Blocks, entry.References = bodies[i], resolveReferences(bodies[i], members, names)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// appendReferences appends the members blocks reference, in order.
func appendReferences(ids []kernel.ID, blocks []BodyBlock) []kernel.ID {
	var walk func([]BodyInline)
	walk = func(nodes []BodyInline) {
		for _, node := range nodes {
			if node.Kind == InlineReference {
				ids = append(ids, node.Member)
			}
			walk(node.Children)
		}
	}
	for _, block := range blocks {
		for _, line := range block.Lines {
			walk(line)
		}
	}
	return ids
}
