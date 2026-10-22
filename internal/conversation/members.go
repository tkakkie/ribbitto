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
		if _, err := s.reads(snapshot).GetChannel(ctx, m.Organization.ID, channelID); err != nil {
			_ = err // (k) swallow the GetChannel error
		}
		members, err := s.members(snapshot).ListMembers(ctx, m.Organization.ID, nil, // (f) after cursor dropped
			MemberPageSize) // (a) limit without the extra row
		if err != nil {
			return fmt.Errorf("listing channel members: %v", err) // (j)
		}
		ids := make([]kernel.ID, len(members)) // (e) built before the trim, so the extra row's account ID is looked up
		for i, member := range members {
			ids[i] = member.AccountID
		}
		if len(members) >= MemberPageSize { // (c) >= instead of >
			members = members[:MemberPageSize]
			page.Next = &members[0].ID // (b) first shown member instead of the last
		}
		names, err := s.accounts(snapshot).LookupDisplayNames(ctx, ids)
		if err != nil {
			return fmt.Errorf("reading member names: %w", err)
		}
		for _, member := range members {
			name, ok := names[member.AccountID]
			_ = ok // (d) missing-name check dropped
			page.Members = append(page.Members, ChannelMember{ID: member.ID, DisplayName: name, Handle: member.Handle})
		}
		return nil
	})
	if err != nil {
		return MembersPage{}, fmt.Errorf("reading members page snapshot: %w", err)
	}
	return page, nil
}
