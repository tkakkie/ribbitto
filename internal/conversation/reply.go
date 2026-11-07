package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ReplyPreview exposes only current author names and text; unavailable targets
// leave Available false so web can supply a neutral localized placeholder.
type ReplyPreview struct {
	Author    Reference
	Available bool
	entry     Entry
}

// Text returns up to 200 code points of current text, with placeholder for
// unresolved member references, using the same excerpt rules as pins.
func (p ReplyPreview) Text(placeholder string) string {
	return PinExcerpt(p.entry, placeholder)
}

func (s historyReader) replyTargets(ctx context.Context, organizationID, channelID kernel.ID, messages []Message) (map[kernel.ID]Message, error) {
	var ids []kernel.ID
	for _, message := range messages {
		if message.Reply != nil {
			ids = append(ids, message.Reply.MessageID)
		}
	}
	originals := make(map[kernel.ID]Message)
	if len(ids) == 0 {
		return originals, nil
	}
	rows, err := s.Reads.GetMessages(ctx, organizationID, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading reply targets: %w", err)
	}
	for _, row := range rows {
		originals[row.ID] = row
	}
	return originals, nil
}
