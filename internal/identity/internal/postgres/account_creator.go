package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// AccountCreator inserts accounts inside a caller's transaction.
type AccountCreator struct{ queries *sqlcgen.Queries }

// AccountCreatorIn binds an AccountCreator to tx without managing its lifecycle.
func AccountCreatorIn(tx platform.Tx) *AccountCreator {
	return &AccountCreator{queries: sqlcgen.New(pgxbridge.Tx(tx))}
}

// CreateAccount inserts validated, normalised values and a password hash.
func (c *AccountCreator) CreateAccount(ctx context.Context, email, displayName, passwordHash string) (kernel.ID, error) {
	row, err := c.queries.CreateAccount(ctx, sqlcgen.CreateAccountParams{
		Email: email, DisplayName: displayName, PasswordHash: passwordHash,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" && pgErr.ConstraintName == "account_email_key" {
				return kernel.ID{}, identity.ErrEmailTaken
			}
			// The NFC CHECK is account_email_check1; all email CHECKs share this prefix.
			if pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "account_email_") {
				return kernel.ID{}, identity.ErrInvalidEmail
			}
		}
		return kernel.ID{}, fmt.Errorf("creating account: %w", err)
	}
	return row.ID.Bytes, nil
}
