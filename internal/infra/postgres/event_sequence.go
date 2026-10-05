package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// EventSequence is what posting and branching need from org inside their
// transaction: the organisation's next event_seq, taken first, whose row
// lock makes sequence order commit order (decision 5). An unknown
// organisation is org.ErrNotFound. The consumers move to conversation in
// step 4.
type EventSequence interface {
	NextEventSeq(ctx context.Context, organizationID domain.ID) (int64, error)
}

// EventSequenceIn binds an EventSequence to a flow's transaction.
type EventSequenceIn func(platform.Tx) EventSequence
