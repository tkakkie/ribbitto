package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// OpenPool connects the pgx pool that the sqlc queries use. Migrations keep
// using Open's database/sql handle, which goose needs.
func OpenPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is empty")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("creating database pool: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return pool, nil
}

// SessionStore implements auth.SessionStore.
type SessionStore struct {
	queries *sqlcgen.Queries
}

// NewSessionStore returns a SessionStore that runs its queries on db.
func NewSessionStore(db sqlcgen.DBTX) *SessionStore {
	return &SessionStore{queries: sqlcgen.New(db)}
}

// CreateSession stores a session by its token hash.
func (s *SessionStore) CreateSession(ctx context.Context, tokenHash []byte, accountID domain.ID, expiresAt time.Time) error {
	_, err := s.queries.CreateSession(ctx, sqlcgen.CreateSessionParams{
		TokenHash: tokenHash,
		AccountID: pgtype.UUID{Bytes: accountID, Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("inserting session: %w", err)
	}
	return nil
}

// SessionAccount returns the account of a session that expires after now.
func (s *SessionStore) SessionAccount(ctx context.Context, tokenHash []byte, now time.Time) (domain.Account, error) {
	row, err := s.queries.GetSessionByTokenHash(ctx, sqlcgen.GetSessionByTokenHashParams{
		TokenHash: tokenHash,
		Now:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, auth.ErrNoSession
	}
	if err != nil {
		return domain.Account{}, fmt.Errorf("selecting session: %w", err)
	}
	return domain.Account{ID: row.Account.ID.Bytes, Email: row.Account.Email, DisplayName: row.Account.DisplayName}, nil
}

// DeleteSession deletes the session with this token hash, if any.
func (s *SessionStore) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if err := s.queries.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions deletes sessions that expired before the given time.
func (s *SessionStore) DeleteExpiredSessions(ctx context.Context, before time.Time) error {
	if err := s.queries.DeleteExpiredSessions(ctx, pgtype.Timestamptz{Time: before, Valid: true}); err != nil {
		return fmt.Errorf("deleting expired sessions: %w", err)
	}
	return nil
}
