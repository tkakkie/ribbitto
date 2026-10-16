package postgres

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// SetSendKey implements identity.PreferenceStore.
func (s *AccountStore) SetSendKey(ctx context.Context, accountID kernel.ID, key identity.SendKey) error {
	updated, err := s.queries.SetAccountSendKey(ctx, sqlcgen.SetAccountSendKeyParams{ID: uuid(accountID), SendKey: string(key)})
	if err != nil {
		return fmt.Errorf("updating send key: %w", err)
	}
	if updated == 0 {
		return identity.ErrNoAccount
	}
	return nil
}
