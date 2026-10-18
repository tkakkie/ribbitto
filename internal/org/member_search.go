package org

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"golang.org/x/text/unicode/norm"
)

// MemberCandidate identifies a member offered for a mention.
type MemberCandidate struct {
	ID          kernel.ID
	Handle      string
	DisplayName string
}

// MemberSearchStore lists only the organisation's members, in handle order.
type MemberSearchStore interface {
	ListSearchMembers(context.Context, kernel.ID) ([]ListedMember, error)
}

// MemberSearchStoreIn binds the search listing to its snapshot.
type MemberSearchStoreIn func(platform.Snapshot) MemberSearchStore

// SearchAccountsIn binds identity's display-name directory to the search snapshot.
type SearchAccountsIn func(platform.Snapshot) identity.Directory

// SnapshotRunner runs a use case's reads in one read-only, repeatable-read snapshot.
type SnapshotRunner interface {
	InSnapshot(context.Context, func(platform.Snapshot) error) error
}

// MemberSearch finds mention candidates within a resolved membership's organisation.
type MemberSearch struct {
	runner   SnapshotRunner
	members  MemberSearchStoreIn
	accounts SearchAccountsIn
}

// NewMemberSearch constructs a search with snapshot-bound member and name reads.
func NewMemberSearch(runner SnapshotRunner, members MemberSearchStoreIn, accounts SearchAccountsIn) *MemberSearch {
	return &MemberSearch{runner: runner, members: members, accounts: accounts}
}

// Search returns at most eight members, preferring handle and display-name word
// prefixes over other substrings, then ordering by handle. The caller supplies
// the membership resolved from the session and URL, never a submitted organisation.
// Queries are trimmed, lose one leading @, and count at most 50 NFC code points;
// longer queries return no candidates. Matching uses NFKC and Unicode lower case.
func (s *MemberSearch) Search(ctx context.Context, m Membership, query string) (candidates []MemberCandidate, err error) {
	query = norm.NFC.String(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if utf8.RuneCountInString(query) > 50 {
		return nil, nil
	}
	query = foldMemberSearch(query)
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		members, err := s.members(snapshot).ListSearchMembers(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("listing search members: %w", err)
		}
		ids := make([]kernel.ID, len(members))
		for i, member := range members {
			ids[i] = member.AccountID
		}
		names, err := s.accounts(snapshot).LookupDisplayNames(ctx, ids)
		if err != nil {
			return fmt.Errorf("looking up search display names: %w", err)
		}
		// The listing is handle-ordered. Keeping eight of each rank preserves
		// tie order and bounds result memory while matching stays linear.
		var ranked [2][]MemberCandidate
		for _, member := range members {
			name := names[member.AccountID]
			if identity.IsBlankLookingName(name) {
				name = ""
			}
			handle, display := foldMemberSearch(member.Handle), foldMemberSearch(name)
			if !strings.Contains(handle, query) && !strings.Contains(display, query) {
				continue
			}
			rank := 1
			if strings.HasPrefix(handle, query) || strings.HasPrefix(display, query) {
				rank = 0
			} else {
				for _, part := range strings.Fields(display) {
					if strings.HasPrefix(part, query) {
						rank = 0
						break
					}
				}
			}
			if len(ranked[rank]) < 8 {
				ranked[rank] = append(ranked[rank], MemberCandidate{ID: member.ID, Handle: member.Handle, DisplayName: name})
			}
		}
		candidates = append(ranked[0], ranked[1]...)
		if len(candidates) > 8 {
			candidates = candidates[:8]
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("searching members: %w", err)
	}
	return candidates, nil
}

func foldMemberSearch(value string) string {
	return strings.ToLower(norm.NFKC.String(value))
}
