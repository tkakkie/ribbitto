package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres/sqlcgen"
)

// Reader reads realtime's durable event log, without delivery authorization.
type Reader struct {
	pool   *pgxpool.Pool
	bounds realtime.BoundsIn
	kinds  realtime.Kinds
}

// NewReader returns a reader on pool that reads org's cursor bounds through
// bounds and routes the kinds registered in kinds.
func NewReader(pool *pgxpool.Pool, bounds realtime.BoundsIn, kinds realtime.Kinds) *Reader {
	return &Reader{pool: pool, bounds: bounds, kinds: kinds}
}

// EventsAfter returns at most limit events for the organisation, in sequence
// order strictly after after. Unregistered kinds keep only their envelope; a
// Router's error fails the batch. Limit must be nonnegative. The bounds and
// rows share one read-only snapshot; a cursor outside the inclusive bounds
// returns realtime.ErrCursorExpired, including when limit is zero, and an
// unknown organisation has an empty batch.
func (r *Reader) EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]realtime.Event, error) {
	events := []realtime.Event{}
	err := platform.InSnapshot(ctx, r.pool, func(snapshot platform.Snapshot) error {
		boundary, committed, found, err := r.bounds(snapshot).EventBounds(ctx, organizationID)
		if err != nil {
			return fmt.Errorf("reading event bounds: %w", err)
		}
		if !found {
			return nil
		}
		if after < boundary || after > committed {
			return realtime.ErrCursorExpired
		}
		rows, err := sqlcgen.New(pgxbridge.Snapshot(snapshot)).EventsAfter(ctx, sqlcgen.EventsAfterParams{
			OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, AfterSeq: after, BatchLimit: int64(limit),
		})
		if err != nil {
			return fmt.Errorf("reading events: %w", err)
		}
		for _, row := range rows {
			event, err := r.eventFromRow(organizationID, row)
			if err != nil {
				return fmt.Errorf("reading event %d: %w", row.Seq, err)
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// eventFromRow routes registered kinds through their Routers; the payload
// stays encoded for the kind's publisher.
func (r *Reader) eventFromRow(organizationID domain.ID, row sqlcgen.EventsAfterRow) (realtime.Event, error) {
	event := realtime.Event{OrganizationID: organizationID, Seq: row.Seq, Kind: realtime.EventKind(row.Kind)}
	if row.AudienceMemberID.Valid {
		id := domain.ID(row.AudienceMemberID.Bytes)
		event.AudienceMemberID = &id
	}
	route, ok := r.kinds[event.Kind]
	if !ok {
		return event, nil // Unregistered kinds keep their envelope; streams skip them.
	}
	var err error
	if event.ChannelID, event.Topics, err = route(row.Data); err != nil {
		return realtime.Event{}, fmt.Errorf("routing %s data: %w", event.Kind, err)
	}
	event.Payload = row.Data
	return event, nil
}
