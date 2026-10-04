package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// One returns a message with current author names, or ErrMessageNotFound.
// The caller resolves membership and channel access before reading, as for
// Before.
func (s Reader) One(ctx context.Context, m org.Membership, channelID kernel.ID, eventSeq int64) (Entry, error) {
	msg, err := s.History.GetMessage(ctx, m.Organization.ID, channelID, eventSeq)
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
func (s Reader) Many(ctx context.Context, m org.Membership, channelID kernel.ID, ids []kernel.ID) ([]Entry, error) {
	messages, err := s.History.GetMessages(ctx, m.Organization.ID, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	if len(messages) != len(ids) {
		return nil, ErrMessageNotFound
	}
	return s.entries(ctx, m, channelID, messages)
}
