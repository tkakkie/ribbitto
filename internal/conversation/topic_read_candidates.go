package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// SequenceRange contains message sequences from Lo inclusive to Hi exclusive.
type SequenceRange struct{ Lo, Hi int64 }

// TopicReadCandidates supplies unread message bounds without exposing storage.
type TopicReadCandidates interface {
	// Ranges returns [previous + 1, next) for the topic's unread messages shown
	// through cursor, using whole-channel neighbours (0 or message + 1 if absent).
	// Prefix is the read prefix's exclusive end; readSet contains ranges above
	// it. Floor skips older messages unless moved in after it. The caller must
	// validate cursor against committed events in the same transaction.
	Ranges(ctx context.Context, organizationID, channelID, topicID kernel.ID, cursor, prefix, floor int64, readSet []SequenceRange) ([]SequenceRange, error)
}
