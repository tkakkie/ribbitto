package realtime

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Bounds reads an organisation's cursor bounds, which org owns (decision
// 26): the replay boundary and the committed event_seq. found is false for
// an unknown organisation.
type Bounds interface {
	EventBounds(ctx context.Context, organizationID domain.ID) (boundary, committed int64, found bool, err error)
}

// BoundsIn binds Bounds to the reader's snapshot, so a batch's bounds and
// rows describe one moment.
type BoundsIn func(platform.Snapshot) Bounds
