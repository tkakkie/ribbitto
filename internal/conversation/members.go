package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// MemberPageSize bounds the members shown and resolved on each page.
const MemberPageSize = 100

// ChannelMember holds a channel member's names for display, never authorization.
type ChannelMember struct {
	ID                  kernel.ID
	DisplayName, Handle string
}

// MembersPage is one bounded page of a channel's members, read in one snapshot,
// and the member ID the next page starts after (nil on the last page).
type MembersPage struct {
	Members []ChannelMember
	Next    *kernel.ID
}

// Members reads a bounded channel membership page. Public channels currently
// include every organisation member; this use case is the seam for private channels.
// It reads no sidebar, counts or stream cursor: the panel renders beside a
// conversation page that has its own (#903).
func (s *Reader) Members(ctx context.Context, m org.Membership, channelID kernel.ID, after *kernel.ID) (page MembersPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		// The listing is organisation-wide, so the channel check is what
		// keeps another organisation's or a missing channel out.
		if _, err := s.reads(snapshot).GetChannel(ctx, m.Member.OrganizationID, channelID); err != nil {
			return fmt.Errorf("finding channel: %w", err)
		}
		members, err := s.members(snapshot).ListMembers(ctx, m.Organization.ID, after, MemberPageSize+1)
		if err != nil {
			return fmt.Errorf("listing channel members: %w", err)
		}
		if len(members) > MemberPageSize {
			members = members[:MemberPageSize]
			page.Next = &members[len(members)-1].ID
		}
		ids := make([]kernel.ID, len(members))
		for i, member := range members {
			ids[i] = member.AccountID
		}
		names, err := s.accounts(snapshot).LookupDisplayNames(ctx, ids)
		if err != nil {
			return fmt.Errorf("reading member names: %w", err)
		}
		for _, member := range members {
			name, ok := names[member.AccountID]
			if !ok {
				return fmt.Errorf("missing name for member %x", member.ID)
			}
			page.Members = append(page.Members, ChannelMember{ID: member.ID, DisplayName: name, Handle: member.Handle})
		}
		return nil
	})
	if err != nil {
		return MembersPage{}, fmt.Errorf("reading members page snapshot: %w", err)
	}
	return page, nil
}
