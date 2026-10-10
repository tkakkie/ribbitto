package unread

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ChannelReadState contains the prefix end and up to 100 unread gaps.
// Gap bounds are inclusive Lo and exclusive Hi; a truncated set has no open gap.
type ChannelReadState struct {
	Prefix int64
	Gaps   []Range
}

// ChannelRanges reads the first ranges of the sidebar's channels in one statement.
type ChannelRanges interface {
	Read(context.Context, kernel.ID, kernel.ID, int64, []kernel.ID) (map[kernel.ID]ChannelReadState, error)
}

// ChannelRangesIn binds range reads to the caller's snapshot.
type ChannelRangesIn func(platform.Snapshot) ChannelRanges
