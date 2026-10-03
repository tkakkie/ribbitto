package setup

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
)

// ErrToken rejects an incorrect token, including any unconfigured token.
var ErrToken = errors.New("invalid setup token")

// ErrCompleted means installation setup has already succeeded.
var ErrCompleted = errors.New("setup already completed")

// ValidationErrors associates invalid fields with their validation errors.
type ValidationErrors map[string]error

// Error describes validation failure without exposing submitted values.
func (ValidationErrors) Error() string { return "invalid setup fields" }

// Input contains the submitted fields, before normalization.
type Input struct {
	OrganizationName, Slug, Email, DisplayName, Handle, Password string
}

// Result identifies the organization and account created by setup.
type Result struct {
	OrganizationID, AccountID domain.ID
}

// Store persists setup atomically; Create must return ErrCompleted for losers.
type Store interface {
	Open(context.Context) (bool, error)
	Create(ctx context.Context, organizationName, slug, email, displayName, handle, passwordHash string) (Result, error)
}

// Service controls first-run setup. Share the process's password hasher.
type Service struct {
	store  Store
	hasher *identity.Hasher
	token  string
}

// New constructs a setup service with the configured token.
func New(store Store, hasher *identity.Hasher, token string) *Service {
	return &Service{store: store, hasher: hasher, token: token}
}

// Open reports whether the installation has no completed setup row.
func (s *Service) Open(ctx context.Context) (bool, error) {
	open, err := s.store.Open(ctx)
	if err != nil {
		return false, fmt.Errorf("checking setup: %w", err)
	}
	return open, nil
}

// Complete validates before hashing; the store commits all rows or none.
func (s *Service) Complete(ctx context.Context, token string, input Input) (Result, error) {
	configured, submitted := sha256.Sum256([]byte(s.token)), sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(configured[:], submitted[:]) != 1 || s.token == "" {
		return Result{}, ErrToken
	}
	open, err := s.Open(ctx)
	if err != nil {
		return Result{}, err
	}
	if !open {
		return Result{}, ErrCompleted
	}
	fields := ValidationErrors{}
	for _, field := range []struct {
		name     string
		value    *string
		validate func(string) (string, error)
	}{
		{"organization_name", &input.OrganizationName, domain.ValidateOrganizationName},
		{"slug", &input.Slug, domain.ValidateSlug},
		{"email", &input.Email, domain.ValidateEmail},
		{"display_name", &input.DisplayName, domain.ValidateDisplayName},
		{"handle", &input.Handle, domain.ValidateHandle},
		{"password", &input.Password, domain.ValidatePassword},
	} {
		value, err := field.validate(*field.value)
		if err != nil {
			fields[field.name] = err
		}
		*field.value = value
	}
	if len(fields) != 0 {
		return Result{}, fields
	}
	hash, err := s.hasher.Hash(ctx, input.Password)
	if err != nil {
		return Result{}, fmt.Errorf("hashing setup password: %w", err)
	}
	result, err := s.store.Create(ctx, input.OrganizationName, input.Slug, input.Email, input.DisplayName, input.Handle, hash)
	if err != nil {
		return Result{}, fmt.Errorf("creating setup: %w", err)
	}
	return result, nil
}
