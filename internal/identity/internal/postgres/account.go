package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// AccountStore implements identity.AccountStore.
type AccountStore struct {
	queries *sqlcgen.Queries
}

// NewAccountStore returns an AccountStore that runs its queries on pool.
func NewAccountStore(pool *pgxpool.Pool) *AccountStore {
	return &AccountStore{queries: sqlcgen.New(pool)}
}

// AccountCredentials returns the ID and password hash of the account with
// this normalised email.
func (s *AccountStore) AccountCredentials(ctx context.Context, email string) (domain.ID, string, error) {
	row, err := s.queries.GetAccountCredentialsByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ID{}, "", identity.ErrNoAccount
	}
	if err != nil {
		return domain.ID{}, "", fmt.Errorf("selecting account: %w", err)
	}
	return row.ID.Bytes, row.PasswordHash, nil
}

// Directory implements identity.Directory inside a caller's snapshot. It
// reads only display names, never credentials.
type Directory struct {
	queries *sqlcgen.Queries
}

// NewDirectoryIn returns a Directory bound to the caller's snapshot, so its
// display-name batch reads the same state as the caller's other reads.
func NewDirectoryIn(snapshot platform.Snapshot) *Directory {
	return &Directory{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

// LookupDisplayNames returns the display names of the accounts that exist.
func (d *Directory) LookupDisplayNames(ctx context.Context, ids []domain.ID) (map[domain.ID]string, error) {
	rows, err := d.queries.LookupDisplayNames(ctx, uuidArray(ids))
	if err != nil {
		return nil, fmt.Errorf("looking up display names: %w", err)
	}
	result := make(map[domain.ID]string, len(rows))
	for _, row := range rows {
		result[row.ID.Bytes] = row.DisplayName
	}
	return result, nil
}

// uuidArray is a copy of the legacy store's helper; modules share no store
// code (decision 26).
func uuidArray(ids []domain.ID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	return result
}
