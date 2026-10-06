package org_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
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
	org, id kernel.ID
	handle  string
}

func (s *store) UpdateHandle(_ context.Context, org, id kernel.ID, handle string) error {
	s.calls++
	s.org, s.id, s.handle = org, id, handle
	return s.err
}

func TestChangeHandle(t *testing.T) {
	alice := &identity.Account{ID: kernel.ID{1}}
	membership := org.Membership{
		Organization: org.Organization{ID: kernel.ID{2}, Slug: "acme"},
		Member:       org.Member{ID: kernel.ID{3}, OrganizationID: kernel.ID{2}, AccountID: alice.ID},
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
		{name: "another account", account: &identity.Account{ID: kernel.ID{9}}, slug: "acme", handle: "alice", wantErr: org.ErrNotFound},
		{name: "another organisation", account: alice, slug: "globex", handle: "alice", wantErr: org.ErrNotFound},
		{name: "non-member with an invalid handle", account: &identity.Account{ID: kernel.ID{9}}, slug: "acme", handle: "!", wantErr: org.ErrNotFound},
		{name: "invalid", account: alice, slug: "acme", handle: "a", wantErr: org.ErrInvalidHandle},
		{name: "reserved", account: alice, slug: "acme", handle: "Everyone", wantErr: org.ErrInvalidHandle},
		{name: "look-alike", account: alice, slug: "acme", handle: "Kelvin", wantErr: org.ErrInvalidHandle},
		{name: "taken", account: alice, slug: "acme", handle: "bob", storeErr: org.ErrHandleTaken, wantErr: org.ErrHandleTaken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &store{err: tt.storeErr}
			got, err := org.NewHandleChanger(authorizer{membership}, s).ChangeHandle(t.Context(), tt.account, tt.slug, tt.handle)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
			// Only an authorised, valid request reaches the store, and only
			// for the caller's own membership.
			reached := tt.wantErr == nil || errors.Is(tt.wantErr, org.ErrHandleTaken)
			if reached != (s.calls == 1) || reached && (s.org != membership.Organization.ID || s.id != membership.Member.ID) {
				t.Fatalf("store calls %d for org %v member %v", s.calls, s.org, s.id)
			}
		})
	}
}
