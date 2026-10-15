package conversation

import (
	"context"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// MaxChannelPins bounds a channel's curated list across all its topics.
const MaxChannelPins = 50

// PinState is current message metadata; both fields are zero when unpinned.
type PinState struct {
	PinnedByMemberID kernel.ID
	PinnedAt         time.Time
}

// PinnedMessage references current content, never a retained excerpt.
type PinnedMessage struct {
	Message
	PinState
}

// PinWriter uses the caller's transaction without taking locks. The caller
// locks the organisation first and enforces the limit and no-op rules.
// Inspect, set and clear return ErrMessageNotFound for missing/out-of-scope IDs.
type PinWriter interface {
	GetMessagePin(ctx context.Context, organizationID, channelID, messageID kernel.ID) (PinState, error)
	CountChannelPins(ctx context.Context, organizationID, channelID kernel.ID) (int64, error)
	SetMessagePin(ctx context.Context, organizationID, channelID, messageID, pinnerID kernel.ID) error
	ClearMessagePin(ctx context.Context, organizationID, channelID, messageID kernel.ID) error
}

// PinReader reads at most MaxChannelPins current messages, newest pin first
// then message ID. Topics and names use batches in the same caller snapshot.
type PinReader interface {
	ListChannelPins(ctx context.Context, organizationID, channelID kernel.ID) ([]PinnedMessage, error)
}
