package signup

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrClosed means registration is disabled or setup is incomplete.
var ErrClosed = errors.New("sign-up closed")

// ErrEmailTaken means an account already uses the normalized email.
var ErrEmailTaken = errors.New("email already registered")

// ErrHandleTaken means another member of the organisation already uses the
// handle, ignoring case.
var ErrHandleTaken = errors.New("handle already taken")

// ValidationErrors associates invalid fields with their validation errors.
type ValidationErrors = setup.ValidationErrors

// Store reads setup availability and registers an account and member atomically.
type Store interface {
	Open(context.Context) (bool, error) // True until setup completes.
	SignUp(ctx context.Context, displayName, handle, email, passwordHash string) (domain.ID, error)
}

// Service shares the process's hasher and reads installation setup state.
type Service struct {
	store   Store
	hasher  *auth.Hasher
	enabled bool
}

// New constructs a registration service with the operator's switch.
func New(store Store, hasher *auth.Hasher, enabled bool) *Service {
	return &Service{store: store, hasher: hasher, enabled: enabled}
}

// Open reports whether registration is enabled and setup has completed.
func (s *Service) Open(ctx context.Context) (bool, error) {
	if !s.enabled {
		return false, nil
	}
	open, err := s.store.Open(ctx)
	return !open && err == nil, err
}

// SignUp validates before hashing and commits the account and member together.
func (s *Service) SignUp(ctx context.Context, displayName, handle, email, password string) (domain.ID, error) {
	open, err := s.Open(ctx)
	if err != nil {
		return domain.ID{}, fmt.Errorf("checking sign-up: %w", err)
	}
	if !open {
		return domain.ID{}, ErrClosed
	}
	fields := ValidationErrors{}
	displayName, fields["display_name"] = domain.ValidateDisplayName(displayName)
	handle, fields["handle"] = domain.ValidateHandle(handle)
	email, fields["email"] = domain.ValidateEmail(email)
	password, fields["password"] = domain.ValidatePassword(password)
	for name, err := range fields {
		if err == nil {
			delete(fields, name)
		}
	}
	if len(fields) != 0 {
		return domain.ID{}, fields
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return domain.ID{}, fmt.Errorf("hashing sign-up password: %w", err)
	}
	return s.store.SignUp(ctx, displayName, handle, email, hash)
}
