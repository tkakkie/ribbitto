package message

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
)

// Entry is a stored message with its current author names and topic label.
type Entry struct {
	conversation.Message
	DisplayName, Handle string
	TopicName           string
	DefaultTopic        bool
}

// History reads messages within one organisation and channel. Lists are
// newest first, optionally filtered by topic; GetMessage returns conversation.ErrMessageNotFound
// when the scoped key is absent. GetMessages returns the requested IDs only,
// newest first; missing or out-of-scope IDs are omitted.
type History interface {
	GetMessages(context.Context, domain.ID, domain.ID, []domain.ID) ([]conversation.Message, error)
	ListMessagesBefore(context.Context, domain.ID, domain.ID, *domain.ID, *int64, int32) ([]conversation.Message, error)
	GetMessage(context.Context, domain.ID, domain.ID, int64) (conversation.Message, error)
}

// Reader composes history with org, identity and topic's exported directory APIs.
type Reader struct {
	History  History
	Members  org.Directory
	Accounts identity.Directory
	Topics   topic.Directory
}

// PageSize is how many messages one page of history holds.
const PageSize = 50

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
	Topic    *conversation.Topic
	Topics   []conversation.Topic
	Current  conversation.Channel
	Channels []conversation.Channel
	// EventCursor is the snapshot's organisation sequence; nil on older pages.
	EventCursor *int64
}

// Before returns the page of messages older than event_seq before, or the
// latest page when before is nil. A nil topicID includes every topic.
// The caller resolves membership and the
// channel through org.Authorizer and channel before reading. before is only an upper
// bound: the query is scoped to the membership's organisation and the
// channel, so a value taken from another channel cannot reach its messages.
func (s Reader) Before(ctx context.Context, m org.Membership, channelID domain.ID, topicID *domain.ID, before *int64) (Page, error) {
	// One extra row says whether an older page exists without a count query.
	messages, err := s.History.ListMessagesBefore(ctx, m.Organization.ID, channelID, topicID, before, PageSize+1)
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

// entries adds topic labels and author names, returning messages oldest first.
func (s Reader) entries(ctx context.Context, m org.Membership, channelID domain.ID, messages []conversation.Message) ([]Entry, error) {
	ids := make([]domain.ID, 0, len(messages))
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
	topics, err := s.Topics.LookupTopics(ctx, m.Organization.ID, channelID, ids)
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
