package member_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
)

type authorizer struct{ membership org.Membership }

func (a authorizer) Member(_ context.Context, account *identity.Account, slug string) (org.Membership, error) {
	if account == nil || account.ID != a.membership.Member.AccountID || slug != "acme" {
		return org.Membership{}, org.ErrNotFound
	}
	return a.membership, nil
}

type store struct {
	err     error
	calls   int
	org, id domain.ID
	handle  string
}

func (s *store) UpdateHandle(_ context.Context, org, id domain.ID, handle string) error {
	s.calls++
	s.org, s.id, s.handle = org, id, handle
	return s.err
}

func TestChangeHandle(t *testing.T) {
	alice := &identity.Account{ID: domain.ID{1}}
	membership := org.Membership{
		Organization: domain.Organization{ID: domain.ID{2}, Slug: "acme"},
		Member:       domain.Member{ID: domain.ID{3}, OrganizationID: domain.ID{2}, AccountID: alice.ID},
	}
	for _, tt := range []struct {
		name     string
		account  *identity.Account
		slug     string
		handle   string
		storeErr error
		want     string
		wantErr  error
	}{
		{name: "normalised", account: alice, slug: "acme", handle: " Alice ", want: "alice"},
		{name: "signed out", slug: "acme", handle: "alice", wantErr: org.ErrNotFound},
		{name: "another account", account: &identity.Account{ID: domain.ID{9}}, slug: "acme", handle: "alice", wantErr: org.ErrNotFound},
		{name: "another organisation", account: alice, slug: "globex", handle: "alice", wantErr: org.ErrNotFound},
		{name: "non-member with an invalid handle", account: &identity.Account{ID: domain.ID{9}}, slug: "acme", handle: "!", wantErr: org.ErrNotFound},
		{name: "invalid", account: alice, slug: "acme", handle: "a", wantErr: member.ErrInvalidHandle},
		{name: "reserved", account: alice, slug: "acme", handle: "Everyone", wantErr: member.ErrInvalidHandle},
		{name: "look-alike", account: alice, slug: "acme", handle: "Kelvin", wantErr: member.ErrInvalidHandle},
		{name: "taken", account: alice, slug: "acme", handle: "bob", storeErr: member.ErrHandleTaken, wantErr: member.ErrHandleTaken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &store{err: tt.storeErr}
			got, err := member.New(authorizer{membership}, s).ChangeHandle(t.Context(), tt.account, tt.slug, tt.handle)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
			// Only an authorised, valid request reaches the store, and only
			// for the caller's own membership.
			reached := tt.wantErr == nil || errors.Is(tt.wantErr, member.ErrHandleTaken)
			if reached != (s.calls == 1) || reached && (s.org != membership.Organization.ID || s.id != membership.Member.ID) {
				t.Fatalf("store calls %d for org %v member %v", s.calls, s.org, s.id)
			}
		})
	}
}
