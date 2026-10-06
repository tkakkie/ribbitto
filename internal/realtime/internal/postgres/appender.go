package postgres

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres/sqlcgen"
)

// Appender writes events inside the writer's transaction, so an event
// commits or rolls back with its entity and sequence. It never reads the
// payload: the kind's publisher encoded it.
type Appender struct{ queries *sqlcgen.Queries }

// AppenderIn binds an Appender to tx.
func AppenderIn(tx platform.Tx) *Appender {
	return &Appender{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// Append records an event at the sequence the caller already allocated. A
// nil audience is organisation-wide; otherwise only that member, of the same
// organisation, may receive it.
func (a *Appender) Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	params := sqlcgen.InsertEventParams{
		OrganizationID: uuid(organizationID), Seq: seq, Kind: string(kind), Data: payload,
	}
	if audience != nil {
		params.AudienceMemberID = uuid(*audience)
	}
	if err := a.queries.InsertEvent(ctx, params); err != nil {
		return fmt.Errorf("appending %s event: %w", kind, err)
	}
	return nil
}
