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

// MembersPage is the channel context, sidebar, members and stream cursor from one snapshot.
type MembersPage struct {
	Current     Channel
	Channels    []Channel
	Topics      []Topic
	Members     []ChannelMember
	Next        *kernel.ID
	EventCursor *int64
}

// Members reads a bounded channel membership page. Public channels currently
// include every organisation member; this use case is the seam for private channels.
func (s *Reader) Members(ctx context.Context, m org.Membership, channelID kernel.ID, after *kernel.ID) (page MembersPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		page.Current, err = reads.GetChannel(ctx, m.Organization.ID, channelID)
		if err != nil {
			return fmt.Errorf("finding channel: %w", err)
		}
		page.Channels, err = reads.ListChannels(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("listing channels: %w", err)
		}
		page.Topics, err = reads.ListTopics(ctx, m.Organization.ID, channelID, sidebarTopics)
		if err != nil {
			return fmt.Errorf("listing topics: %w", err)
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
		seq, err := s.cursor(snapshot).EventSeq(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("reading members page cursor: %w", err)
		}
		page.EventCursor = &seq
		return nil
	})
	if err != nil {
		return MembersPage{}, fmt.Errorf("reading members page snapshot: %w", err)
	}
	return page, nil
}
