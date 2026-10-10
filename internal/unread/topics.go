package unread

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// TopicCounter counts messages through a snapshot-bound conversation API.
type TopicCounter interface {
	Count(context.Context, kernel.ID, kernel.ID, int64, []kernel.ID, []int64, [][2]int64, *kernel.ID) (map[kernel.ID]int64, int64, error)
}

// TopicCounterIn binds topic counts to the caller's snapshot.
type TopicCounterIn func(platform.Snapshot) TopicCounter

// TopicCounts combines topic state and message counts in two snapshot statements.
type TopicCounts struct {
	state    TopicStateReaderIn
	messages TopicCounterIn
}

// NewTopicCounts builds topic counts with snapshot-bound factories.
func NewTopicCounts(state TopicStateReaderIn, messages TopicCounterIn) *TopicCounts {
	return &TopicCounts{state: state, messages: messages}
}

// Read returns capped counts and the selected topic's uncapped first unread
// sequence (zero if none). Scope and joined come from resolved membership.
func (c *TopicCounts) Read(ctx context.Context, s platform.Snapshot, scope Scope, joined int64, listed []kernel.ID, selected *kernel.ID) (map[kernel.ID]int64, int64, error) {
	state, err := c.state(s).Read(ctx, scope, joined, listed, selected)
	if err != nil {
		return nil, 0, fmt.Errorf("loading topic count state: %w", err)
	}
	ids, floors := make([]kernel.ID, 0, len(state.Floors)), make([]int64, 0, len(state.Floors))
	for id, floor := range state.Floors {
		ids, floors = append(ids, id), append(floors, floor)
	}
	read := make([][2]int64, len(state.Ranges))
	for i, r := range state.Ranges {
		read[i] = [2]int64{r.Lo, r.Hi}
	}
	counts, first, err := c.messages(s).Count(ctx, scope.OrganizationID, scope.ChannelID, state.Prefix, ids, floors, read, selected)
	if err != nil {
		return nil, 0, fmt.Errorf("reading topic counts: %w", err)
	}
	return counts, first, nil
}
