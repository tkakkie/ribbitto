package org_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// fakeStore holds memberships by (account, slug) and the setup
// organisation's slug.
type fakeStore struct {
	memberships map[domain.ID]map[string]org.Membership
	home        string
	err         error
}

func (f fakeStore) Membership(_ context.Context, accountID domain.ID, slug string) (org.Membership, error) {
	if f.err != nil {
		return org.Membership{}, f.err
	}
	m, ok := f.memberships[accountID][slug]
	if !ok {
		return org.Membership{}, org.ErrNotFound
	}
	return m, nil
}

func (f fakeStore) HomeSlug(_ context.Context, accountID domain.ID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if _, ok := f.memberships[accountID][f.home]; !ok {
		return "", org.ErrNotFound
	}
	return f.home, nil
}

func TestAuthorizer(t *testing.T) {
	alice := &identity.Account{ID: domain.ID{1}} // member of acme
	bob := &identity.Account{ID: domain.ID{2}}   // member of globex only
	carol := &identity.Account{ID: domain.ID{3}} // no membership
	acme := org.Membership{Organization: domain.Organization{ID: domain.ID{10}, Slug: "acme"}, Member: domain.Member{Role: domain.RoleOwner}}
	globex := org.Membership{Organization: domain.Organization{ID: domain.ID{11}, Slug: "globex"}}
	store := fakeStore{
		memberships: map[domain.ID]map[string]org.Membership{alice.ID: {"acme": acme}, bob.ID: {"globex": globex}},
		home:        "acme",
	}
	broken := errors.New("connection refused")
	for _, tt := range []struct {
		name    string
		store   fakeStore
		account *identity.Account
		slug    string
		want    error
		home    error
	}{
		{"member", store, alice, "acme", nil, nil},
		{"signed out", store, nil, "acme", org.ErrNotFound, org.ErrNotFound},
		{"no membership", store, carol, "acme", org.ErrNotFound, org.ErrNotFound},
		{"member of another organisation", store, bob, "acme", org.ErrNotFound, org.ErrNotFound},
		{"unknown slug", store, alice, "initech", org.ErrNotFound, nil},
		{"impossible slug", store, alice, "Not A Slug", org.ErrNotFound, nil},
		{"store error", fakeStore{err: broken}, alice, "acme", broken, broken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := org.NewAuthorizer(tt.store)
			got, err := a.Member(t.Context(), tt.account, tt.slug)
			if !errors.Is(err, tt.want) || (tt.want == nil && got != acme) {
				t.Fatalf("Member = %+v, %v; want %v", got, err, tt.want)
			}
			if tt.want == nil && errors.Is(err, org.ErrNotFound) {
				t.Fatal("store error reported as not found")
			}
			slug, err := a.HomeSlug(t.Context(), tt.account)
			if !errors.Is(err, tt.home) || (tt.home == nil && slug != "acme") {
				t.Fatalf("HomeSlug = %q, %v; want %v", slug, err, tt.home)
			}
		})
	}
}

func TestMayReceive(t *testing.T) {
	aliceMember, otherMember := domain.ID{20}, domain.ID{21}
	acme := org.Membership{Organization: domain.Organization{ID: domain.ID{10}, Slug: "acme"}, Member: domain.Member{ID: aliceMember}}
	store := fakeStore{memberships: map[domain.ID]map[string]org.Membership{{1}: {"acme": acme}}}
	event := realtime.Event{OrganizationID: acme.Organization.ID, Seq: 5, Kind: message.KindPosted}
	withAudience := func(member domain.ID) realtime.Event {
		e := event
		e.AudienceMemberID = &member
		return e
	}
	otherOrganisation := event
	otherOrganisation.OrganizationID = domain.ID{11}
	broken := errors.New("connection refused")
	for _, tt := range []struct {
		name    string
		store   fakeStore
		account domain.ID
		event   realtime.Event
		want    bool
		wantErr error
	}{
		{"member, organisation-wide", store, domain.ID{1}, event, true, nil},
		{"member, own audience", store, domain.ID{1}, withAudience(aliceMember), true, nil},
		{"member, another member's audience", store, domain.ID{1}, withAudience(otherMember), false, nil},
		{"membership lost", store, domain.ID{2}, event, false, nil},
		{"event of another organisation", store, domain.ID{1}, otherOrganisation, false, nil},
		// A failed lookup must not look like a deny, or the stream would skip
		// the event for good.
		{"lookup fails", fakeStore{err: broken}, domain.ID{1}, event, false, broken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, kind := range []realtime.EventKind{message.KindPosted, topic.KindMessagesMoved} {
				event := tt.event
				event.Kind = kind
				got, err := org.NewAuthorizer(tt.store).MayReceive(t.Context(), tt.account, "acme", event)
				if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
					t.Fatalf("MayReceive(%s) = %v, %v; want %v, %v", kind, got, err, tt.want, tt.wantErr)
				}
			}
		})
	}
}
