package message

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// Entry is a stored message with its current author names, ready for a view.
type Entry struct {
	domain.Message
	DisplayName, Handle string
}

// History reads messages newest first, within one organisation and channel.
type History interface {
	ListMessagesBefore(context.Context, domain.ID, domain.ID, *int64, int32) ([]domain.Message, error)
}

// Reader composes history with org and identity's exported directory APIs.
type Reader struct {
	History  History
	Members  member.Directory
	Accounts auth.Directory
}

// Latest returns the latest 50 messages oldest first. The caller resolves
// membership and the channel through authz and channel before reading.
func (s Reader) Latest(ctx context.Context, m authz.Membership, channelID domain.ID) ([]Entry, error) {
	messages, err := s.History.ListMessagesBefore(ctx, m.Organization.ID, channelID, nil, 50)
	if err != nil {
		return nil, fmt.Errorf("reading history: %w", err)
	}
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
	entries := make([]Entry, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		author, ok := members[msg.MemberID]
		name, named := names[author.AccountID]
		if !ok || !named {
			return nil, fmt.Errorf("missing author for message %x", msg.ID)
		}
		entries = append(entries, Entry{Message: msg, DisplayName: name, Handle: author.Handle})
	}
	return entries, nil
}
