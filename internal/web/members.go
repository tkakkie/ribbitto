package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// members and overview render the conversation with that panel open, so
// opening, switching and closing a panel are ordinary page loads.
func (p channelPages) members(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.panel = view.PanelMembers
	p.show(w, r, m)
}

func (p channelPages) overview(w http.ResponseWriter, r *http.Request, m org.Membership) {
	p.panel = view.PanelOverview
	p.show(w, r, m)
}

// memberListing reads one page of members after the conversation's snapshot.
// The page keeps that snapshot's cursor, so replay follows what the
// conversation shows; presence has its own token from the same members.
func (p channelPages) memberListing(ctx context.Context, m org.Membership, id kernel.ID) (*view.MemberPage, error) {
	result, err := p.messages.Members(ctx, m, id, p.after)
	if err != nil {
		return nil, fmt.Errorf("reading members: %w", err)
	}
	listing := &view.MemberPage{Next: result.Next}
	ids := make([]kernel.ID, len(result.Members))
	for i, member := range result.Members {
		ids[i] = member.ID
		listing.Items = append(listing.Items, view.Member{ID: member.ID, DisplayName: memberDisplayName(member.DisplayName), Handle: member.Handle})
	}
	if p.stream != nil && p.stream.Presence != nil {
		snapshot, err := p.stream.Presence.Read(m.Organization.ID, ids)
		if err != nil {
			return nil, fmt.Errorf("reading presence snapshot: %w", err)
		}
		listing.PresenceAfter = presenceToken(snapshot.Token)
		for i, online := range snapshot.Online {
			listing.Items[i].Online = online
		}
	}
	return listing, nil
}

// viewPins maps the overview's pins, read in the page's snapshot; live
// delivery (#1205) replaces the list from its own snapshots.
func viewPins(pinned []conversation.Pin) []view.Pin {
	pins := make([]view.Pin, len(pinned))
	for i, pin := range pinned {
		pins[i] = view.Pin{
			MessageID: pin.ID, Excerpt: pin.Excerpt,
			AuthorDisplayName: memberDisplayName(pin.DisplayName), AuthorHandle: pin.Handle,
			Topic:             view.Topic{Name: pin.TopicName, IsDefault: pin.DefaultTopic},
			PinnerDisplayName: memberDisplayName(pin.PinnerName), PinnerHandle: pin.PinnerHandle,
			PinnedAt: pin.PinnedAt,
		}
	}
	return pins
}
