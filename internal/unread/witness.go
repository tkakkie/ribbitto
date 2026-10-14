package unread

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// PersistedRange is one stored range, including the zero-based prefix row.
type PersistedRange struct {
	ChannelID kernel.ID
	Lo, Hi    int64
}

// PersistedState contains all requested stored rows; missing floors stay absent.
type PersistedState struct {
	Ranges []PersistedRange
	Floors map[kernel.ID]int64
}

// PersistedReader reads only resolved member scope and permitted channel/topic IDs.
type PersistedReader interface {
	Read(context.Context, Scope, []kernel.ID, []kernel.ID) (PersistedState, error)
}
