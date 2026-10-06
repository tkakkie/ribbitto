package org

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// ErrNotFound means the caller may not see the organisation — whether it
// does not exist, the caller is not a member or nobody is signed in. The
// three look the same so that responses never reveal which organisations
// exist.
var ErrNotFound = errors.New("not found")

// Membership is a member together with its organisation.
type Membership struct {
	Organization Organization
	Member       Member
}

// MembershipStore looks memberships up. Every query is scoped to one organisation.
type MembershipStore interface {
	// Membership returns the account's membership in the organisation with
	// this slug, or ErrNotFound.
	Membership(ctx context.Context, accountID kernel.ID, slug string) (Membership, error)
	// HomeSlug returns the slug of the organisation created at setup if the
	// account is its member, or ErrNotFound.
	HomeSlug(ctx context.Context, accountID kernel.ID) (string, error)
}

// Authorizer resolves the caller's membership.
type Authorizer struct {
	store MembershipStore
}

// NewAuthorizer returns an Authorizer backed by store.
func NewAuthorizer(store MembershipStore) *Authorizer {
	return &Authorizer{store: store}
}

// Member returns the membership of the signed-in account (nil when signed
// out) in the organisation named by slug, which comes from the URL.
func (a *Authorizer) Member(ctx context.Context, account *identity.Account, slug string) (Membership, error) {
	if account == nil {
		return Membership{}, ErrNotFound
	}
	// A slug that cannot exist is not worth a query.
	if _, err := ValidateSlug(slug); err != nil {
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
func (a *Authorizer) HomeSlug(ctx context.Context, account *identity.Account) (string, error) {
	if account == nil {
		return "", ErrNotFound
	}
	slug, err := a.store.HomeSlug(ctx, account.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("resolving home organisation: %w", err)
	}
	return slug, err
}

// MayReceive reports whether the account may still receive a durable event,
// checked again immediately before a stream sends it. It satisfies
// realtime.Authorizer. A lost membership, an event of
// another organisation, or an audience that names another member is a deny
// (false, nil). A failed lookup is an error, never a deny: the stream must
// stop and retry, not skip an event the member may be allowed to see.
func (a *Authorizer) MayReceive(ctx context.Context, accountID kernel.ID, organizationSlug string, event realtime.Event) (bool, error) {
	m, err := a.Member(ctx, &identity.Account{ID: accountID}, organizationSlug)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if m.Organization.ID != event.OrganizationID {
		return false, nil
	}
	if event.AudienceMemberID != nil && *event.AudienceMemberID != m.Member.ID {
		return false, nil
	}
	return true, nil
}
