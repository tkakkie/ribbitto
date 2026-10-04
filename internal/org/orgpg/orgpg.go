package orgpg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// BoundsIn reads org's cursor bounds for realtime's reader, in the reader's
// snapshot.
func BoundsIn(snapshot platform.Snapshot) realtime.Bounds { return postgres.BoundsIn(snapshot) }

// NewSequences returns the committed-sequence reader realtime's watermark
// raises the hub with.
func NewSequences(pool *pgxpool.Pool) realtime.SequenceReader { return postgres.NewSequences(pool) }

// RetentionBoundaryIn locks an organisation and raises its replay boundary
// for realtime's cleaner, in the cleaner's transaction.
func RetentionBoundaryIn(tx platform.Tx) realtime.RetentionBoundary {
	return postgres.RetentionBoundaryIn(tx)
}

// EventKinds returns org's routers for registration with realtime's reader.
func EventKinds() realtime.Kinds {
	return realtime.Kinds{org.KindJoined: org.RouteJoined}
}
