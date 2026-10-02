package message

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrNotFound means no message has this event_seq in the caller's organisation
// and channel, including when the message exists in another scope.
var ErrNotFound = errors.New("message not found")

// One returns a message with current author names, or ErrNotFound. The caller
// resolves membership and channel access before reading, as for Before.
func (s Reader) One(ctx context.Context, m authz.Membership, channelID domain.ID, eventSeq int64) (Entry, error) {
	msg, err := s.History.GetMessage(ctx, m.Organization.ID, channelID, eventSeq)
	if err != nil {
		return Entry{}, fmt.Errorf("reading message: %w", err)
	}
	entries, err := s.entries(ctx, m, channelID, []domain.Message{msg})
	if err != nil {
		return Entry{}, err
	}
	return entries[0], nil
}
