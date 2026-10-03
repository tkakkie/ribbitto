package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventReader reads realtime's durable event log, without delivery
// authorization, and the committed sequences the watermark check raises
// the hub to.
type EventReader struct {
	queries *sqlcgen.Queries
	kinds   realtime.Kinds
}

// NewEventReader returns a reader using db that routes the kinds registered
// in kinds.
func NewEventReader(db sqlcgen.DBTX, kinds realtime.Kinds) *EventReader {
	return &EventReader{queries: sqlcgen.New(db), kinds: kinds}
}

// EventKinds returns the publishers' Routers for the kinds written today, for
// wiring and tests, until each module registers its own (steps 3 and 4).
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		realtime.EventMessagePosted: message.RoutePosted,
		realtime.EventMemberJoined:  member.RouteJoined,
		realtime.EventMessagesMoved: topic.RouteMoved,
	}
}

// EventsAfter returns at most limit events for the organisation, in sequence
// order strictly after after. Unknown kinds retain only their envelope;
// malformed data for known kinds fails the batch. Limit must be nonnegative.
// The replay boundary, committed event_seq and rows share one snapshot;
// a cursor outside those inclusive bounds returns realtime.ErrCursorExpired,
// including when limit is zero.
func (r *EventReader) EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]realtime.Event, error) {
	rows, err := r.queries.EventsAfter(ctx, sqlcgen.EventsAfterParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, AfterSeq: after, BatchLimit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	}
	events := make([]realtime.Event, 0, len(rows))
	for _, row := range rows {
		if after < row.EventLogBoundarySeq || after > row.EventSeq {
			return nil, realtime.ErrCursorExpired
		}
		if row.Seq == 0 {
			continue // Both cursor bounds must be returned even when the log is empty.
		}
		event, err := r.eventFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("reading event %d: %w", row.Seq, err)
		}
		events = append(events, event)
	}
	return events, nil
}

// CommittedSequences returns the committed event_seq of each given
// organisation that exists, in one query; it reads org's shared-kernel
// watermark and writes nothing.
func (r *EventReader) CommittedSequences(ctx context.Context, organizations []domain.ID) (map[domain.ID]int64, error) {
	ids := make([]pgtype.UUID, len(organizations))
	for i, org := range organizations {
		ids[i] = pgtype.UUID{Bytes: org, Valid: true}
	}
	rows, err := r.queries.CommittedSequences(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading committed sequences: %w", err)
	}
	seqs := make(map[domain.ID]int64, len(rows))
	for _, row := range rows {
		seqs[row.ID.Bytes] = row.EventSeq
	}
	return seqs, nil
}

// eventFromRow routes registered kinds through their Routers; the payload
// stays encoded for the kind's publisher.
func (r *EventReader) eventFromRow(row sqlcgen.EventsAfterRow) (realtime.Event, error) {
	event := realtime.Event{OrganizationID: row.OrganizationID.Bytes, Seq: row.Seq, Kind: realtime.EventKind(row.Kind)}
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
