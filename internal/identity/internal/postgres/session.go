package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// SessionStore implements identity.SessionStore.
type SessionStore struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

// NewSessionStore returns a SessionStore that runs its queries on pool.
func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool, queries: sqlcgen.New(pool)}
}

// ReplaceSession deletes the old session and stores the new one in one
// transaction, and reports the deleted session's id, if there was one.
func (s *SessionStore) ReplaceSession(ctx context.Context, oldHash, newHash []byte, accountID kernel.ID, expiresAt time.Time) (ended kernel.ID, found bool, err error) {
	err = platform.InTx(ctx, s.pool, func(tx platform.Tx) error {
		q := sqlcgen.New(pgxbridge.Tx(tx))
		ids, err := q.DeleteSessionByTokenHash(ctx, oldHash)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			ended, found = ids[0].Bytes, true
		}
		return q.CreateSession(ctx, sqlcgen.CreateSessionParams{
			TokenHash: newHash,
			AccountID: pgtype.UUID{Bytes: accountID, Valid: true},
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
	})
	if err != nil {
		// Sessions.Replace adds "replacing session"; the store adds no prefix.
		return kernel.ID{}, false, withoutDetail(err)
	}
	return ended, found, nil
}

// CreateSession stores a session by its token hash.
func (s *SessionStore) CreateSession(ctx context.Context, tokenHash []byte, accountID kernel.ID, expiresAt time.Time) error {
	err := s.queries.CreateSession(ctx, sqlcgen.CreateSessionParams{
		TokenHash: tokenHash,
		AccountID: pgtype.UUID{Bytes: accountID, Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("inserting session: %w", withoutDetail(err))
	}
	return nil
}

// withoutDetail drops a PostgreSQL error's Detail, which for a unique
// violation on token_hash would carry the hash into logs; token hashes
// must never be logged. The code and constraint name are kept.
func withoutDetail(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	return fmt.Errorf("%s (SQLSTATE %s, constraint %q)", pgErr.Message, pgErr.Code, pgErr.ConstraintName)
}

// SessionAccount returns the account of a session that expires after now,
// with the session's id and expiry.
func (s *SessionStore) SessionAccount(ctx context.Context, tokenHash []byte, now time.Time) (identity.Account, identity.Session, error) {
	row, err := s.queries.GetSessionByTokenHash(ctx, sqlcgen.GetSessionByTokenHashParams{
		TokenHash: tokenHash,
		Now:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Account{}, identity.Session{}, identity.ErrNoSession
	}
	if err != nil {
		return identity.Account{}, identity.Session{}, fmt.Errorf("selecting session: %w", err)
	}
	return identity.Account{ID: row.ID.Bytes, Email: row.Email, DisplayName: row.DisplayName},
		identity.Session{ID: row.SessionID.Bytes, ExpiresAt: row.ExpiresAt.Time}, nil
}

// DeleteSession deletes the session with this token hash, if any, and
// reports its id.
func (s *SessionStore) DeleteSession(ctx context.Context, tokenHash []byte) (ended kernel.ID, found bool, err error) {
	ids, err := s.queries.DeleteSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		// Sessions.Delete adds "deleting session"; the store adds no prefix.
		return kernel.ID{}, false, err
	}
	if len(ids) == 0 {
		return kernel.ID{}, false, nil
	}
	return ids[0].Bytes, true, nil
}

// DeleteExpiredSessions deletes sessions that expired before the given time.
func (s *SessionStore) DeleteExpiredSessions(ctx context.Context, before time.Time) error {
	if err := s.queries.DeleteExpiredSessions(ctx, pgtype.Timestamptz{Time: before, Valid: true}); err != nil {
		return fmt.Errorf("deleting expired sessions: %w", err)
	}
	return nil
}
