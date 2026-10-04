package org

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrSignUpClosed means registration is disabled or setup is incomplete.
var ErrSignUpClosed = errors.New("sign-up closed")

// ErrEmailTaken means an account already uses the normalized email.
var ErrEmailTaken = errors.New("email already registered")

// SignUp shares the process's hasher and owns the registration transaction.
type SignUp struct {
	state    SetupState
	runner   TxRunner
	writes   RegistrationWriterIn
	accounts AccountCreatorIn
	events   EventAppenderIn
	hasher   *identity.Hasher
	enabled  bool
}

// NewSignUp constructs a registration service with transaction-bound writers.
func NewSignUp(state SetupState, runner TxRunner, writes RegistrationWriterIn, accounts AccountCreatorIn, events EventAppenderIn, hasher *identity.Hasher, enabled bool) *SignUp {
	return &SignUp{state: state, runner: runner, writes: writes, accounts: accounts, events: events, hasher: hasher, enabled: enabled}
}

// Open reports whether registration is enabled and setup has completed.
func (s *SignUp) Open(ctx context.Context) (bool, error) {
	if !s.enabled {
		return false, nil
	}
	open, err := s.state.Open(ctx)
	if err != nil {
		return false, fmt.Errorf("checking sign-up: %w", err)
	}
	return !open, nil
}

// SignUp validates before hashing and commits the account and member together.
func (s *SignUp) SignUp(ctx context.Context, displayName, handle, email, password string) (domain.ID, error) {
	open, err := s.Open(ctx)
	if err != nil {
		return domain.ID{}, err
	}
	if !open {
		return domain.ID{}, ErrSignUpClosed
	}
	fields := ValidationErrors{}
	validateRegistration(&displayName, &handle, &email, &password, fields)
	if len(fields) != 0 {
		return domain.ID{}, fields
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return domain.ID{}, fmt.Errorf("hashing sign-up password: %w", err)
	}
	var id domain.ID
	err = s.runner.InTx(ctx, func(tx platform.Tx) error {
		writes := s.writes(tx)
		organizationID, err := writes.SetupOrganization(ctx)
		if err != nil {
			return err
		}
		// Lock the organisation before either account or member is written.
		seq, err := writes.NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		id, err = s.accounts(tx).CreateAccount(ctx, email, displayName, hash)
		if err != nil {
			return err
		}
		memberID, err := writes.CreateMember(ctx, organizationID, id, RoleMember, seq, handle)
		if err != nil {
			return err
		}
		return s.events(tx).Append(ctx, organizationID, seq, KindJoined, nil, EncodeJoined(memberID))
	})
	mapped := registrationError(err)
	switch {
	case errors.Is(err, identity.ErrEmailTaken):
		return domain.ID{}, ErrEmailTaken
	case errors.Is(err, ErrHandleTaken):
		return domain.ID{}, ErrHandleTaken
	case mapped != nil:
		return domain.ID{}, mapped
	case errors.Is(err, ErrSignUpClosed):
		return domain.ID{}, ErrSignUpClosed
	case err != nil:
		return domain.ID{}, fmt.Errorf("storing sign-up transaction: %w", err)
	}
	return id, nil
}
