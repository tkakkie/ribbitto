package unread

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// TopicReadState contains the prefix end, read ranges above it and topic floors.
// A topic without a stored floor has floor Prefix - 1.
type TopicReadState struct {
	Prefix int64
	Ranges []Range
	Floors map[kernel.ID]int64
}

// TopicStateReader loads a channel's read state in one snapshot statement.
type TopicStateReader interface {
	// Read deduplicates listed topics and the optional selected topic, accepting
	// at most 51 distinct IDs. Scope and joined come from resolved membership.
	Read(ctx context.Context, scope Scope, joined int64, listed []kernel.ID, selected *kernel.ID) (TopicReadState, error)
}

// TopicStateReaderIn binds topic read-state loading to the caller's snapshot.
type TopicStateReaderIn func(platform.Snapshot) TopicStateReader
