package identitypg

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// NewSessions returns the session lifecycle on pool with the given clock.
// A non-nil cancel ends a deleted session's open streams.
func NewSessions(pool *pgxpool.Pool, now func() time.Time, cancel identity.SessionCanceller) *identity.Sessions {
	if cancel == nil {
		return identity.NewSessions(postgres.NewSessionStore(pool), now)
	}
	return identity.NewSessionsWithCanceller(postgres.NewSessionStore(pool), now, cancel)
}

// NewSignIn returns sign-in on pool. hasher is the process's one hasher,
// shared with setup and sign-up (decision 10).
func NewSignIn(pool *pgxpool.Pool, hasher *identity.Hasher, sessions *identity.Sessions) *identity.SignIn {
	return identity.NewSignIn(postgres.NewAccountStore(pool), hasher, sessions)
}

// AccountsIn returns the display-name directory bound to snapshot, for a
// caller that reads authors in the same snapshot as the rest of its page.
func AccountsIn(snapshot platform.Snapshot) identity.Directory {
	return postgres.NewDirectoryIn(snapshot)
}
