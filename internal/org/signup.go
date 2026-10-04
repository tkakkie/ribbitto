package org

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
)

// ErrSignUpClosed means registration is disabled or setup is incomplete.
var ErrSignUpClosed = errors.New("sign-up closed")

// ErrEmailTaken means an account already uses the normalized email.
var ErrEmailTaken = errors.New("email already registered")

// SignUpStore reads setup availability and registers an account and member atomically.
type SignUpStore interface {
	Open(context.Context) (bool, error) // True until setup completes.
	SignUp(ctx context.Context, displayName, handle, email, passwordHash string) (domain.ID, error)
}

// SignUp shares the process's hasher and reads installation setup state.
type SignUp struct {
	store   SignUpStore
	hasher  *identity.Hasher
	enabled bool
}

// NewSignUp constructs a registration service with the operator's switch.
func NewSignUp(store SignUpStore, hasher *identity.Hasher, enabled bool) *SignUp {
	return &SignUp{store: store, hasher: hasher, enabled: enabled}
}

// Open reports whether registration is enabled and setup has completed.
func (s *SignUp) Open(ctx context.Context) (bool, error) {
	if !s.enabled {
		return false, nil
	}
	open, err := s.store.Open(ctx)
	return !open && err == nil, err
}

// SignUp validates before hashing and commits the account and member together.
func (s *SignUp) SignUp(ctx context.Context, displayName, handle, email, password string) (domain.ID, error) {
	open, err := s.Open(ctx)
	if err != nil {
		return domain.ID{}, fmt.Errorf("checking sign-up: %w", err)
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
	return s.store.SignUp(ctx, displayName, handle, email, hash)
}
