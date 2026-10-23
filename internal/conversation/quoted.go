package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

// QuoteReadError says which quoted message could not be read, so the log of
// a failed quote preview names it without the caller repeating the ID.
type QuoteReadError struct {
	MessageID kernel.ID
	Err       error
}

func (e *QuoteReadError) Error() string {
	return fmt.Sprintf("reading quoted message %x: %v", e.MessageID, e.Err)
}

// Quoted reads the one message a quote refers to, with its author and topic
// labels, as Many would for a batch of one. A message missing, or outside the
// member's organisation and the channel, is ErrMessageNotFound, so the preview
// of an unknown or another channel's message is the same 404.
func (s *Reader) Quoted(ctx context.Context, m org.Membership, channelID, id kernel.ID) (Entry, error) {
	entries, err := s.Many(ctx, m, channelID, []kernel.ID{id})
	if err != nil {
		return Entry{}, &QuoteReadError{MessageID: id, Err: err}
	}
	return entries[0], nil
}
