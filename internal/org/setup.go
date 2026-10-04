package org

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
)

// ErrSetupToken rejects an incorrect token, including any unconfigured token.
var ErrSetupToken = errors.New("invalid setup token")

// ErrSetupCompleted means installation setup has already succeeded.
var ErrSetupCompleted = errors.New("setup already completed")

// ValidationErrors associates invalid fields with their validation errors.
type ValidationErrors map[string]error

// Error describes validation failure without exposing submitted values.
func (ValidationErrors) Error() string { return "invalid setup fields" }

// SetupInput contains the submitted fields, before normalization.
type SetupInput struct {
	OrganizationName, Slug, Email, DisplayName, Handle, Password string
}

// SetupResult identifies the organization and account created by setup.
type SetupResult struct {
	OrganizationID, AccountID domain.ID
}

// SetupStore persists setup atomically; Create must return ErrSetupCompleted
// for losers.
type SetupStore interface {
	Open(context.Context) (bool, error)
	Create(ctx context.Context, organizationName, slug, email, displayName, handle, passwordHash string) (SetupResult, error)
}

// Setup controls first-run setup. Share the process's password hasher.
type Setup struct {
	store  SetupStore
	hasher *identity.Hasher
	token  string
}

// NewSetup constructs a setup service with the configured token.
func NewSetup(store SetupStore, hasher *identity.Hasher, token string) *Setup {
	return &Setup{store: store, hasher: hasher, token: token}
}

// Open reports whether the installation has no completed setup row.
func (s *Setup) Open(ctx context.Context) (bool, error) {
	open, err := s.store.Open(ctx)
	if err != nil {
		return false, fmt.Errorf("checking setup: %w", err)
	}
	return open, nil
}

// Complete validates before hashing; the store commits all rows or none.
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
	result, err := s.store.Create(ctx, input.OrganizationName, input.Slug, input.Email, input.DisplayName, input.Handle, hash)
	if err != nil {
		return SetupResult{}, fmt.Errorf("creating setup: %w", err)
	}
	return result, nil
}

// validateRegistration checks the account fields setup and sign-up share,
// keeping each flow's form keys, and normalises the valid ones in place.
func validateRegistration(displayName, handle, email, password *string, fields ValidationErrors) {
	*displayName, fields["display_name"] = identity.ValidateDisplayName(*displayName)
	*handle, fields["handle"] = ValidateHandle(*handle)
	*email, fields["email"] = identity.ValidateEmail(*email)
	*password, fields["password"] = identity.ValidatePassword(*password)
	for name, err := range fields {
		if err == nil {
			delete(fields, name)
		}
	}
}
