package org

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrSetupToken rejects an incorrect token, including any unconfigured token.
var ErrSetupToken = errors.New("invalid setup token")

// ErrSetupCompleted means installation setup has already succeeded.
var ErrSetupCompleted = errors.New("setup already completed")

// SetupInput contains the submitted fields, before normalization.
type SetupInput struct {
	OrganizationName, Slug, Email, DisplayName, Handle, Password string
}

// SetupResult identifies the organization and account created by setup.
type SetupResult struct {
	OrganizationID, AccountID domain.ID
}

// Setup controls first-run setup. Share the process's password hasher.
type Setup struct {
	state    SetupState
	runner   TxRunner
	writes   RegistrationWriterIn
	accounts AccountCreatorIn
	events   EventAppenderIn
	channels DefaultChannelCreatorIn
	hasher   *identity.Hasher
	token    string
}

// NewSetup constructs a setup service with the configured token and
// transaction-bound writers.
func NewSetup(state SetupState, runner TxRunner, writes RegistrationWriterIn, accounts AccountCreatorIn, events EventAppenderIn, channels DefaultChannelCreatorIn, hasher *identity.Hasher, token string) *Setup {
	return &Setup{state: state, runner: runner, writes: writes, accounts: accounts, events: events, channels: channels, hasher: hasher, token: token}
}

// Open reports whether the installation has no completed setup row.
func (s *Setup) Open(ctx context.Context) (bool, error) {
	open, err := s.state.Open(ctx)
	if err != nil {
		return false, fmt.Errorf("checking setup: %w", err)
	}
	return open, nil
}

// Complete validates before hashing and commits all rows or none.
func (s *Setup) Complete(ctx context.Context, token string, input SetupInput) (SetupResult, error) {
	configured, submitted := sha256.Sum256([]byte(s.token)), sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(configured[:], submitted[:]) != 1 || s.token == "" {
		return SetupResult{}, ErrSetupToken
	}
	open, err := s.Open(ctx)
	if err != nil {
		return SetupResult{}, err
	}
	if !open {
		return SetupResult{}, ErrSetupCompleted
	}
	fields := ValidationErrors{}
	for _, field := range []struct {
		name     string
		value    *string
		validate func(string) (string, error)
	}{
		{"organization_name", &input.OrganizationName, ValidateOrganizationName},
		{"slug", &input.Slug, ValidateSlug},
	} {
		value, err := field.validate(*field.value)
		if err != nil {
			fields[field.name] = err
		}
		*field.value = value
	}
	validateRegistration(&input.DisplayName, &input.Handle, &input.Email, &input.Password, fields)
	if len(fields) != 0 {
		return SetupResult{}, fields
	}
	hash, err := s.hasher.Hash(ctx, input.Password)
	if err != nil {
		return SetupResult{}, fmt.Errorf("hashing setup password: %w", err)
	}
	var result SetupResult
	err = s.runner.InTx(ctx, func(tx platform.Tx) error {
		writes := s.writes(tx)
		organizationID, err := writes.CreateOrganization(ctx, input.OrganizationName, input.Slug)
		if err != nil {
			return err
		}
		seq, err := writes.NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		accountID, err := s.accounts(tx).CreateAccount(ctx, input.Email, input.DisplayName, hash)
		if err != nil {
			return err
		}
		memberID, err := writes.CreateMember(ctx, organizationID, accountID, RoleOwner, seq, input.Handle)
		if err != nil {
			return err
		}
		if err := s.events(tx).Append(ctx, organizationID, seq, KindJoined, nil, EncodeJoined(memberID)); err != nil {
			return err
		}
		// Listed exception (feature map): a completed setup never exists
		// without its default channel; a failure here rolls the organisation
		// back too.
		if err := s.channels(tx).CreateDefaultChannel(ctx, organizationID); err != nil {
			return err
		}
		if err := writes.CompleteSetup(ctx, organizationID); err != nil {
			return err
		}
		result = SetupResult{OrganizationID: organizationID, AccountID: accountID}
		return nil
	})
	if err != nil {
		return SetupResult{}, s.failed(ctx, err)
	}
	return result, nil
}

// failed maps a rolled-back setup transaction's error to setup's result.
func (s *Setup) failed(ctx context.Context, err error) error {
	mapped := registrationError(err)
	switch {
	case mapped != nil:
	case errors.Is(err, ErrSetupCompleted):
		mapped = ErrSetupCompleted
	case errors.Is(err, identity.ErrEmailTaken):
		mapped = ValidationErrors{"email": identity.ErrEmailTaken}
	case errors.Is(err, ErrSlugUnavailable):
		mapped = ValidationErrors{"slug": ErrSlugUnavailable}
	case errors.Is(err, ErrHandleTaken):
		mapped = fmt.Errorf("creating setup: %w", err)
	default:
		return fmt.Errorf("creating setup: %w", err)
	}
	// A concurrent winner may have collided on email or slug first; the
	// loser must still learn that setup is complete, not that a field is.
	if open, checkErr := s.state.Open(ctx); checkErr == nil && !open {
		return ErrSetupCompleted
	}
	return mapped
}
