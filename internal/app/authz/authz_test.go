package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// fakeStore holds memberships by (account, slug) and the setup
// organisation's slug.
type fakeStore struct {
	memberships map[domain.ID]map[string]authz.Membership
	home        string
	err         error
}

func (f fakeStore) Membership(_ context.Context, accountID domain.ID, slug string) (authz.Membership, error) {
	if f.err != nil {
		return authz.Membership{}, f.err
	}
	m, ok := f.memberships[accountID][slug]
	if !ok {
		return authz.Membership{}, authz.ErrNotFound
	}
	return m, nil
}

func (f fakeStore) HomeSlug(_ context.Context, accountID domain.ID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if _, ok := f.memberships[accountID][f.home]; !ok {
		return "", authz.ErrNotFound
	}
	return f.home, nil
}

func TestAuthorizer(t *testing.T) {
	alice := &domain.Account{ID: domain.ID{1}} // member of acme
	bob := &domain.Account{ID: domain.ID{2}}   // member of globex only
	carol := &domain.Account{ID: domain.ID{3}} // no membership
	acme := authz.Membership{Organization: domain.Organization{ID: domain.ID{10}, Slug: "acme"}, Member: domain.Member{Role: domain.RoleOwner}}
	globex := authz.Membership{Organization: domain.Organization{ID: domain.ID{11}, Slug: "globex"}}
	store := fakeStore{
		memberships: map[domain.ID]map[string]authz.Membership{alice.ID: {"acme": acme}, bob.ID: {"globex": globex}},
		home:        "acme",
	}
	broken := errors.New("connection refused")
	for _, tt := range []struct {
		name    string
		store   fakeStore
		account *domain.Account
		slug    string
		want    error
		home    error
	}{
		{"member", store, alice, "acme", nil, nil},
		{"signed out", store, nil, "acme", authz.ErrNotFound, authz.ErrNotFound},
		{"no membership", store, carol, "acme", authz.ErrNotFound, authz.ErrNotFound},
		{"member of another organisation", store, bob, "acme", authz.ErrNotFound, authz.ErrNotFound},
		{"unknown slug", store, alice, "initech", authz.ErrNotFound, nil},
		{"impossible slug", store, alice, "Not A Slug", authz.ErrNotFound, nil},
		{"store error", fakeStore{err: broken}, alice, "acme", broken, broken},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := authz.New(tt.store)
			got, err := a.Member(t.Context(), tt.account, tt.slug)
			if !errors.Is(err, tt.want) || (tt.want == nil && got != acme) {
				t.Fatalf("Member = %+v, %v; want %v", got, err, tt.want)
			}
			if tt.want == nil && errors.Is(err, authz.ErrNotFound) {
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
	acme := authz.Membership{Organization: domain.Organization{ID: domain.ID{10}, Slug: "acme"}, Member: domain.Member{ID: aliceMember}}
	store := fakeStore{memberships: map[domain.ID]map[string]authz.Membership{{1}: {"acme": acme}}}
	event := domain.Event{OrganizationID: acme.Organization.ID, Seq: 5, Kind: domain.EventMessagePosted}
	withAudience := func(member domain.ID) domain.Event {
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
		event   domain.Event
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
			for _, kind := range []domain.EventKind{domain.EventMessagePosted, domain.EventMessagesMoved} {
				event := tt.event
				event.Kind = kind
				got, err := authz.New(tt.store).MayReceive(t.Context(), tt.account, "acme", event)
				if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
					t.Fatalf("MayReceive(%s) = %v, %v; want %v, %v", kind, got, err, tt.want, tt.wantErr)
				}
			}
		})
	}
}
