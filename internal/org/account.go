package org

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// AccountCreator creates identity's account in the caller's transaction.
// Values must already be validated and normalised, and the password hashed.
// The caller owns commit and rollback, including after an error.
type AccountCreator interface {
	CreateAccount(ctx context.Context, email, displayName, passwordHash string) (kernel.ID, error)
}

// AccountCreatorIn binds an account creator to the caller's transaction.
type AccountCreatorIn func(platform.Tx) AccountCreator
