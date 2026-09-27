package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// AccountStore implements auth.AccountStore.
type AccountStore struct {
	queries *sqlcgen.Queries
}

// NewAccountStore returns an AccountStore that runs its queries on db.
func NewAccountStore(db sqlcgen.DBTX) *AccountStore {
	return &AccountStore{queries: sqlcgen.New(db)}
}

// AccountCredentials returns the account with this normalised email and its
// password hash.
func (s *AccountStore) AccountCredentials(ctx context.Context, email string) (domain.Account, string, error) {
	row, err := s.queries.GetAccountByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, "", auth.ErrNoAccount
	}
	if err != nil {
		return domain.Account{}, "", fmt.Errorf("selecting account: %w", err)
	}
	return domain.Account{ID: row.ID.Bytes, Email: row.Email, DisplayName: row.DisplayName}, row.PasswordHash, nil
}
