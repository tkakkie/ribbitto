package orgpg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity"
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

// SequenceIn returns org's event sequence bound to a writer's transaction,
// for posting and branching. Consumers declare the interface they need and
// adapt to it with a closure (decision 26).
func SequenceIn(tx platform.Tx) postgres.Sequence { return postgres.SequenceIn(tx) }

// EventCursorIn returns the organisation's committed event_seq bound to a
// reader's snapshot, for the latest page's cursor. Consumers adapt to it
// with a closure too.
func EventCursorIn(snapshot platform.Snapshot) postgres.EventCursor {
	return postgres.EventCursorIn(snapshot)
}

// NewAuthorizer returns org's authorisation entry point on pool.
func NewAuthorizer(pool *pgxpool.Pool) *org.Authorizer {
	return org.NewAuthorizer(postgres.NewAuthzStore(pool))
}

// NewHandleChanger returns the caller's own handle change on pool.
func NewHandleChanger(pool *pgxpool.Pool) *org.HandleChanger {
	return org.NewHandleChanger(NewAuthorizer(pool), postgres.NewMemberStore(pool))
}

// MembersIn returns the member directory bound to the caller's snapshot.
// Composition roots adapt it to their consumer's factory with a closure.
func MembersIn(snapshot platform.Snapshot) org.Directory {
	return postgres.NewDirectoryIn(snapshot)
}

// NewTxRunner returns the transaction runner setup and sign-up own their
// transaction through, over pool.
func NewTxRunner(pool *pgxpool.Pool) org.TxRunner { return txRunner{pool: pool} }

type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTx(ctx, r.pool, fn)
}

// RegistrationWriterIn returns setup's and sign-up's writes bound to the
// caller's transaction; it is an org.RegistrationWriterIn.
func RegistrationWriterIn(tx platform.Tx) org.RegistrationWriter {
	return postgres.RegistrationWriterIn(tx)
}

// NewSetupState returns the installation's setup state, read on pool.
func NewSetupState(pool *pgxpool.Pool) org.SetupState { return postgres.NewSetupState(pool) }

// EventKinds returns org's routers for registration with realtime's reader.
func EventKinds() realtime.Kinds {
	return realtime.Kinds{org.KindJoined: org.RouteJoined}
}

// NewSignUp wires registration with the shared hasher and injected writers.
func NewSignUp(pool *pgxpool.Pool, hasher *identity.Hasher, enabled bool, accounts org.AccountCreatorIn, events org.EventAppenderIn) *org.SignUp {
	return org.NewSignUp(NewSetupState(pool), NewTxRunner(pool), RegistrationWriterIn, accounts, events, hasher, enabled)
}
