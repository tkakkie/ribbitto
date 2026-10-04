package org

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
)

// ErrInvalidHandle wraps a handle that breaks the rules in domain.ValidateHandle.
var ErrInvalidHandle = errors.New("invalid handle")

// ErrHandleTaken means another member of the organisation already uses the
// handle, ignoring case.
var ErrHandleTaken = errors.New("handle already taken")

// MembershipResolver resolves the signed-in account's membership
// (Authorizer implements it).
type MembershipResolver interface {
	Member(ctx context.Context, account *identity.Account, slug string) (Membership, error)
}

// HandleStore changes a member's handle. It returns ErrHandleTaken when the
// organisation's unique constraint rejects the handle, and ErrNotFound
// when the member no longer exists.
type HandleStore interface {
	UpdateHandle(ctx context.Context, organizationID, memberID domain.ID, handle string) error
}

// HandleChanger runs use cases on the caller's own membership.
type HandleChanger struct {
	authorizer MembershipResolver
	store      HandleStore
}

// NewHandleChanger returns a HandleChanger.
func NewHandleChanger(authorizer MembershipResolver, store HandleStore) *HandleChanger {
	return &HandleChanger{authorizer: authorizer, store: store}
}

// ChangeHandle sets the signed-in account's handle in the organisation named
// by slug (from the URL) and returns the stored, canonical handle. There is
// no member id parameter on purpose: the only member it can change is the
// caller's own. A signed-out caller or a non-member gets ErrNotFound.
// The released handle is free for anyone to take at once.
func (s *HandleChanger) ChangeHandle(ctx context.Context, account *identity.Account, slug, handle string) (string, error) {
	membership, err := s.authorizer.Member(ctx, account, slug)
	if err != nil {
		return "", err
	}
	handle, err = domain.ValidateHandle(handle)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidHandle, err)
	}
	if err := s.store.UpdateHandle(ctx, membership.Organization.ID, membership.Member.ID, handle); err != nil {
		return "", fmt.Errorf("changing handle: %w", err)
	}
	return handle, nil
}
