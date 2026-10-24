package org_test

import (
	"context"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type streamCapacityStore struct {
	org.MembershipStore
	membership      org.Membership
	membershipReads int
	epochOrgID      kernel.ID
	lastSlug        string
}

func (s *streamCapacityStore) AccessEpoch(_ context.Context, organizationID kernel.ID) (int64, error) {
	s.epochOrgID = organizationID
	return 1, nil
}

func (s *streamCapacityStore) Membership(_ context.Context, _ kernel.ID, slug string) (org.Membership, error) {
	s.membershipReads++
	s.lastSlug = slug
	return s.membership, nil
}

func TestCachedAuthorizerCapacity(t *testing.T) {
	organizationID := kernel.ID{1}   // event and membership organisation
	decoyMemberOrgID := kernel.ID{9} // decoy Member.OrganizationID ignored by stream checks
	firstAccountID := kernel.ID{3}
	secondAccountID := kernel.ID{6}

	for _, tc := range []struct {
		name      string
		capacity  int
		wantHits  uint64
		wantReads int
	}{
		{name: "zero uses default capacity", capacity: 0, wantHits: 2, wantReads: 2},
		{name: "negative uses default capacity", capacity: -1, wantHits: 2, wantReads: 2},
		{name: "explicit default capacity", capacity: org.DefaultAuthorizationCapacity, wantHits: 2, wantReads: 2},
		{name: "capacity one evicts previous entry", capacity: 1, wantHits: 0, wantReads: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &streamCapacityStore{
				membership: org.Membership{
					Organization: org.Organization{ID: organizationID, Slug: "acme"},
					Member:       org.Member{OrganizationID: decoyMemberOrgID},
					AccessEpoch:  1,
				},
			}
			authorizer := org.NewCachedAuthorizer(t.Context(), store, tc.capacity)
			event := realtime.Event{OrganizationID: organizationID}
			for _, accountID := range []kernel.ID{firstAccountID, secondAccountID, firstAccountID, secondAccountID} {
				allow, err := authorizer.MayReceive(t.Context(), accountID, "acme", event)
				if !allow || err != nil {
					t.Fatalf("MayReceive(%v) = %t, %v; want true, nil", accountID, allow, err)
				}
			}
			wantStats := org.AuthorizationStats{
				Checks:     4,
				CacheHits:  tc.wantHits,
				EpochReads: 4,
			}
			if got := authorizer.Stats(); got != wantStats {
				t.Fatalf("Stats() = %+v, want %+v", got, wantStats)
			}
			if store.membershipReads != tc.wantReads {
				t.Fatalf("Membership reads = %d, want %d", store.membershipReads, tc.wantReads)
			}
			if store.epochOrgID != organizationID || store.lastSlug != "acme" {
				t.Fatalf("store args org=%v slug=%q, want %v acme", store.epochOrgID, store.lastSlug, organizationID)
			}
		})
	}
}
