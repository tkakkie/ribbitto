package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// BoundsIn reads an organisation's cursor bounds in the reader's snapshot
// (realtime.Bounds).
func BoundsIn(snapshot platform.Snapshot) Bounds {
	return Bounds{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

// Bounds reads organization's replay boundary and committed event_seq.
type Bounds struct{ queries *sqlcgen.Queries }

// EventBounds returns the organisation's replay boundary and committed
// event_seq; found is false when the organisation does not exist.
func (b Bounds) EventBounds(ctx context.Context, organizationID kernel.ID) (boundary, committed int64, found bool, err error) {
	row, err := b.queries.EventBounds(ctx, uuid(organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("selecting event bounds: %w", err)
	}
	return row.EventLogBoundarySeq, row.EventSeq, true, nil
}

// Sequences reads organisations' committed event_seq for the watermark
// (realtime.SequenceReader).
type Sequences struct{ queries *sqlcgen.Queries }

// NewSequences returns the committed-sequence reader on db.
func NewSequences(db sqlcgen.DBTX) *Sequences {
	return &Sequences{queries: sqlcgen.New(db)}
}

// CommittedSequences returns the committed event_seq of each given
// organisation that exists, in one query; it writes nothing.
func (s *Sequences) CommittedSequences(ctx context.Context, organizations []kernel.ID) (map[kernel.ID]int64, error) {
	rows, err := s.queries.CommittedSequences(ctx, uuids(organizations))
	if err != nil {
		return nil, fmt.Errorf("selecting committed sequences: %w", err)
	}
	seqs := make(map[kernel.ID]int64, len(rows))
	for _, row := range rows {
		seqs[row.ID.Bytes] = row.EventSeq
	}
	return seqs, nil
}

// RetentionBoundaryIn locks an organisation and raises its replay boundary
// in the cleaner's transaction (realtime.RetentionBoundary).
func RetentionBoundaryIn(tx platform.Tx) RetentionBoundary {
	return RetentionBoundary{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// RetentionBoundary writes organization's replay boundary for retention.
type RetentionBoundary struct{ queries *sqlcgen.Queries }

// LockForRetention locks the organisation's row, the same lock posting's
// event_seq increment takes, so a batch never interleaves with a commit.
func (b RetentionBoundary) LockForRetention(ctx context.Context, organizationID kernel.ID) error {
	if err := b.queries.LockEventRetentionOrganization(ctx, uuid(organizationID)); err != nil {
		return fmt.Errorf("locking organization for retention: %w", err)
	}
	return nil
}

// RaiseBoundary raises the replay boundary to through, never lowering it.
func (b RetentionBoundary) RaiseBoundary(ctx context.Context, organizationID kernel.ID, through int64) error {
	err := b.queries.RaiseEventLogBoundary(ctx, sqlcgen.RaiseEventLogBoundaryParams{
		OrganizationID: uuid(organizationID), Through: through,
	})
	if err != nil {
		return fmt.Errorf("raising replay boundary: %w", err)
	}
	return nil
}
