package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// SequenceIn binds the organisation's event sequence to a writer's
// transaction.
func SequenceIn(tx platform.Tx) Sequence {
	return Sequence{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// Sequence advances organization's event_seq inside a writer's transaction.
type Sequence struct{ queries *sqlcgen.Queries }

// NextEventSeq takes the organisation's next event_seq. Its UPDATE locks the
// organisation's row until the writer commits, so sequence order is commit
// order (decision 5). An unknown organisation is org.ErrNotFound.
func (s Sequence) NextEventSeq(ctx context.Context, organizationID kernel.ID) (int64, error) {
	seq, err := s.queries.NextEventSeq(ctx, uuid(organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, org.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("taking the next event sequence: %w", err)
	}
	return seq, nil
}

// EventCursorIn binds the organisation's committed event_seq to a reader's
// snapshot.
func EventCursorIn(snapshot platform.Snapshot) EventCursor {
	return EventCursor{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

// EventCursor reads organization's committed event_seq through a bound handle.
type EventCursor struct{ queries *sqlcgen.Queries }

// EventSeq returns the organisation's committed event_seq. An unknown
// organisation is org.ErrNotFound.
func (c EventCursor) EventSeq(ctx context.Context, organizationID kernel.ID) (int64, error) {
	seq, err := c.queries.GetEventSeq(ctx, uuid(organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, org.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("reading the event cursor: %w", err)
	}
	return seq, nil
}

// EventCursorInTx binds the existing cursor query to the caller's transaction.
func EventCursorInTx(tx platform.Tx) EventCursor {
	return EventCursor{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}
