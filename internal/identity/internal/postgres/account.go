package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

// NewAccountStore returns an AccountStore that runs its queries on db.
func NewAccountStore(db sqlcgen.DBTX) *AccountStore {
	return &AccountStore{queries: sqlcgen.New(db)}
}

// NewDirectoryIn returns an AccountStore bound to the caller's snapshot, so
// its display-name batch reads the same state as the caller's other reads.
func NewDirectoryIn(snapshot platform.Snapshot) *AccountStore {
	return NewAccountStore(pgxbridge.Snapshot(snapshot))
}

// AccountCredentials returns the account with this normalised email and its
// password hash.
func (s *AccountStore) AccountCredentials(ctx context.Context, email string) (identity.Account, string, error) {
	row, err := s.queries.GetAccountByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Account{}, "", identity.ErrNoAccount
	}
	if err != nil {
		return identity.Account{}, "", fmt.Errorf("selecting account: %w", err)
	}
	return identity.Account{ID: row.ID.Bytes, Email: row.Email, DisplayName: row.DisplayName}, row.PasswordHash, nil
}

// LookupDisplayNames implements identity.Directory without credentials.
func (s *AccountStore) LookupDisplayNames(ctx context.Context, ids []domain.ID) (map[domain.ID]string, error) {
	rows, err := s.queries.LookupDisplayNames(ctx, uuidArray(ids))
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
