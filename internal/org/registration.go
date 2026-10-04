package org

import (
	"context"
	"errors"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrSlugUnavailable means another organisation already uses the slug, or
// the database rejects it.
var ErrSlugUnavailable = errors.New("slug unavailable")

// TxRunner runs the transaction of a flow that writes several modules' rows,
// such as setup and sign-up (decision 26): the use case owns the boundary,
// the stores only bind to the Tx they are given.
type TxRunner interface {
	// InTx runs fn in a new transaction. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error as is.
	InTx(ctx context.Context, fn func(platform.Tx) error) error
}

// RegistrationWriter writes org's rows for setup and sign-up in the caller's
// transaction. Values must already be validated and normalised. The caller
// owns commit and rollback, including after an error: a failed write leaves
// the transaction unusable.
type RegistrationWriter interface {
	// CreateOrganization returns ErrSlugUnavailable when the slug is
	// taken or invalid.
	CreateOrganization(ctx context.Context, name, slug string) (kernel.ID, error)
	// SetupOrganization returns the organisation the setup row names, or
	// ErrSignUpClosed when setup has not completed.
	SetupOrganization(ctx context.Context) (kernel.ID, error)
	// NextEventSeq takes the organisation's next event_seq and locks its
	// row until commit; an unknown organisation is ErrNotFound.
	NextEventSeq(ctx context.Context, organizationID kernel.ID) (int64, error)
	// CreateMember returns ErrHandleTaken when another member of the
	// organisation uses the handle, and ErrInvalidHandle when the database
	// rejects it.
	CreateMember(ctx context.Context, organizationID, accountID kernel.ID, role Role, joinedEventSeq int64, handle string) (kernel.ID, error)
	// CompleteSetup records the installation's organisation; it returns
	// ErrSetupCompleted when a setup row already exists.
	CompleteSetup(ctx context.Context, organizationID kernel.ID) error
}

// RegistrationWriterIn binds org's registration writes to the caller's
// transaction.
type RegistrationWriterIn func(platform.Tx) RegistrationWriter

// SetupState reads installation-wide state, outside any organisation.
type SetupState interface {
	// Open reports whether setup has not completed yet.
	Open(context.Context) (bool, error)
}
