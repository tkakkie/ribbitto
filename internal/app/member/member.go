package member

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
)

// ErrInvalidHandle wraps a handle that breaks the rules in domain.ValidateHandle.
var ErrInvalidHandle = errors.New("invalid handle")

// ErrHandleTaken means another member of the organisation already uses the
// handle, ignoring case.
var ErrHandleTaken = errors.New("handle already taken")

// Authorizer resolves the signed-in account's membership (authz.Authorizer).
type Authorizer interface {
	Member(ctx context.Context, account *identity.Account, slug string) (authz.Membership, error)
}

// Store changes a member's handle. It returns ErrHandleTaken when the
// organisation's unique constraint rejects the handle, and authz.ErrNotFound
// when the member no longer exists.
type Store interface {
	UpdateHandle(ctx context.Context, organizationID, memberID domain.ID, handle string) error
}

// Service runs use cases on the caller's own membership.
type Service struct {
	authorizer Authorizer
	store      Store
}

// New returns a Service.
func New(authorizer Authorizer, store Store) *Service {
	return &Service{authorizer: authorizer, store: store}
}

// ChangeHandle sets the signed-in account's handle in the organisation named
// by slug (from the URL) and returns the stored, canonical handle. There is
// no member id parameter on purpose: the only member it can change is the
// caller's own. A signed-out caller or a non-member gets authz.ErrNotFound.
// The released handle is free for anyone to take at once.
func (s *Service) ChangeHandle(ctx context.Context, account *identity.Account, slug, handle string) (string, error) {
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
