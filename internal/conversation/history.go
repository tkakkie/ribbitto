package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Entry is a stored message with its current author names and topic label.
type Entry struct {
	Message
	DisplayName, Handle string
	TopicName           string
	DefaultTopic        bool
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

// ChannelPage is a channel, its sidebar and history read in one snapshot.
type ChannelPage struct {
	Page
	Topic    *Topic
	Topics   []Topic
	Current  Channel
	Channels []Channel
	// EventCursor is the snapshot's organisation sequence; nil on older pages.
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

// entries adds topic labels and author names, returning messages oldest first.
func (s historyReader) entries(ctx context.Context, m org.Membership, channelID kernel.ID, messages []Message) ([]Entry, error) {
	ids := make([]kernel.ID, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, msg.MemberID)
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
		entries = append(entries, Entry{Message: msg, DisplayName: name, Handle: author.Handle, TopicName: topic.Name, DefaultTopic: topic.IsDefault})
	}
	return entries, nil
}
