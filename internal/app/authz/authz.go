package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// ErrNotFound means the caller may not see the organisation — whether it
// does not exist, the caller is not a member or nobody is signed in. The
// three look the same so that responses never reveal which organisations
// exist.
var ErrNotFound = errors.New("not found")

// Membership is a member together with its organisation.
type Membership struct {
	Organization domain.Organization
	Member       domain.Member
}

// Store looks memberships up. Every query is scoped to one organisation.
type Store interface {
	// Membership returns the account's membership in the organisation with
	// this slug, or ErrNotFound.
	Membership(ctx context.Context, accountID domain.ID, slug string) (Membership, error)
	// HomeSlug returns the slug of the organisation created at setup if the
	// account is its member, or ErrNotFound.
	HomeSlug(ctx context.Context, accountID domain.ID) (string, error)
}

// Authorizer resolves the caller's membership.
type Authorizer struct {
	store Store
}

// New returns an Authorizer backed by store.
func New(store Store) *Authorizer {
	return &Authorizer{store: store}
}

// Member returns the membership of the signed-in account (nil when signed
// out) in the organisation named by slug, which comes from the URL.
func (a *Authorizer) Member(ctx context.Context, account *domain.Account, slug string) (Membership, error) {
	if account == nil {
		return Membership{}, ErrNotFound
	}
	// A slug that cannot exist is not worth a query.
	if _, err := domain.ValidateSlug(slug); err != nil {
		return Membership{}, ErrNotFound
	}
	membership, err := a.store.Membership(ctx, account.ID, slug)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Membership{}, fmt.Errorf("resolving membership: %w", err)
	}
	return membership, err
}

// HomeSlug returns the organisation that `/` should send the signed-in
// account to: the one created at setup, if the account is a member of it.
// The organisation comes from the setup row, never from the request.
func (a *Authorizer) HomeSlug(ctx context.Context, account *domain.Account) (string, error) {
	if account == nil {
		return "", ErrNotFound
	}
	slug, err := a.store.HomeSlug(ctx, account.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("resolving home organisation: %w", err)
	}
	return slug, err
}
