package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventBoundsIn reads org's cursor bounds for realtime's reader, in the
// reader's snapshot. It reads organization, so it lives here until org's
// module moves (step 3).
func EventBoundsIn(snapshot platform.Snapshot) realtime.Bounds {
	return eventBounds{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

type eventBounds struct{ queries *sqlcgen.Queries }

func (b eventBounds) EventBounds(ctx context.Context, organizationID domain.ID) (int64, int64, bool, error) {
	row, err := b.queries.EventBounds(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return row.EventLogBoundarySeq, row.EventSeq, true, nil
}

// EventSequences reads the committed sequences the watermark check raises
// the hub to (realtime.SequenceReader). It reads organization, so it lives
// here until org's module moves (step 3).
type EventSequences struct{ queries *sqlcgen.Queries }

// NewEventSequences returns the committed-sequence reader on db.
func NewEventSequences(db sqlcgen.DBTX) *EventSequences {
	return &EventSequences{queries: sqlcgen.New(db)}
}

// CommittedSequences returns the committed event_seq of each given
// organisation that exists, in one query; it reads org's event_seq and
// writes nothing.
func (r *EventSequences) CommittedSequences(ctx context.Context, organizations []domain.ID) (map[domain.ID]int64, error) {
	ids := make([]pgtype.UUID, len(organizations))
	for i, org := range organizations {
		ids[i] = pgtype.UUID{Bytes: org, Valid: true}
	}
	rows, err := r.queries.CommittedSequences(ctx, ids)
	if err != nil {
		return nil, err
	}
	seqs := make(map[domain.ID]int64, len(rows))
	for _, row := range rows {
		seqs[row.ID.Bytes] = row.EventSeq
	}
	return seqs, nil
}

// EventKinds returns the publishers' Routers for the kinds written today, for
// wiring and tests, until each module registers its own (steps 3 and 4).
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		message.KindPosted:      message.RoutePosted,
		org.KindJoined:          org.RouteJoined,
		topic.KindMessagesMoved: topic.RouteMoved,
	}
}

// RetentionBoundaryIn locks an organisation and raises its replay boundary
// for realtime's cleaner, in the cleaner's transaction. It writes
// organization, so it lives here until org's module moves (step 3).
func RetentionBoundaryIn(tx platform.Tx) realtime.RetentionBoundary {
	return retentionBoundary{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

type retentionBoundary struct{ queries *sqlcgen.Queries }

func (b retentionBoundary) LockForRetention(ctx context.Context, organizationID domain.ID) error {
	return b.queries.LockEventRetentionOrganization(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
}

func (b retentionBoundary) RaiseBoundary(ctx context.Context, organizationID domain.ID, through int64) error {
	return b.queries.RaiseEventLogBoundary(ctx, sqlcgen.RaiseEventLogBoundaryParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Through: through,
	})
}
