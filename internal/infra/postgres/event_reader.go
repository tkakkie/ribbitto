package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// EventReader reads realtime's durable event log, without delivery
// authorization, and the committed sequences the watermark check raises
// the hub to.
type EventReader struct{ queries *sqlcgen.Queries }

// NewEventReader returns a reader using db.
func NewEventReader(db sqlcgen.DBTX) *EventReader {
	return &EventReader{queries: sqlcgen.New(db)}
}

// EventsAfter returns at most limit events for the organisation, in sequence
// order strictly after after. Unknown kinds retain only their envelope;
// malformed data for known kinds fails the batch. Limit must be nonnegative.
func (r *EventReader) EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]domain.Event, error) {
	rows, err := r.queries.EventsAfter(ctx, sqlcgen.EventsAfterParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, AfterSeq: after, BatchLimit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	}
	events := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		if after < row.EventLogBoundarySeq {
			return nil, domain.ErrCursorExpired
		}
		if row.Seq == 0 {
			continue // The boundary must be returned even when the log is empty.
		}
		event, err := eventFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("reading event %d: %w", row.Seq, err)
		}
		events = append(events, event)
	}
	return events, nil
}

// ExpireEvents deletes old log rows and advances their organisations' boundaries atomically.
func (r *EventReader) ExpireEvents(ctx context.Context, cutoff time.Time) error {
	if err := r.queries.ExpireEvents(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true}); err != nil {
		return fmt.Errorf("expiring events: %w", err)
	}
	return nil
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

func eventFromRow(row sqlcgen.EventsAfterRow) (domain.Event, error) {
	event := domain.Event{OrganizationID: row.OrganizationID.Bytes, Seq: row.Seq, Kind: domain.EventKind(row.Kind)}
	if row.AudienceMemberID.Valid {
		id := domain.ID(row.AudienceMemberID.Bytes)
		event.AudienceMemberID = &id
	}
	// Future kinds may have different payload shapes; the delivery loop skips them.
	if event.Kind != domain.EventMessagePosted && event.Kind != domain.EventMemberJoined {
		return event, nil
	}
	var data struct {
		ChannelID string `json:"channel_id"`
		MessageID string `json:"message_id"`
		MemberID  string `json:"member_id"`
	}
	if err := json.Unmarshal(row.Data, &data); err != nil {
		return domain.Event{}, fmt.Errorf("decoding %s data: %w", event.Kind, err)
	}
	var err error
	switch event.Kind {
	case domain.EventMessagePosted:
		event.ChannelID, err = eventDataID(data.ChannelID)
		if err == nil {
			event.MessageID, err = eventDataID(data.MessageID)
		}
	case domain.EventMemberJoined:
		event.MemberID, err = eventDataID(data.MemberID)
	}
	if err != nil {
		return domain.Event{}, fmt.Errorf("decoding %s data: %w", event.Kind, err)
	}
	return event, nil
}

func eventDataID(value string) (domain.ID, error) {
	// pgtype accepts misplaced UUID separators; persisted payloads use canonical UUIDs.
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return domain.ID{}, fmt.Errorf("required ID is not a canonical UUID")
	}
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return domain.ID{}, fmt.Errorf("parsing event ID: %w", err)
	}
	return id.Bytes, nil
}
