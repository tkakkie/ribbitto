package web

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type typingOwner struct{ state *typing.State }

// NewTypingOwner renders bounded, viewer-filtered snapshots without directory reads.
func NewTypingOwner(state *typing.State) realtime.EphemeralOwner { return typingOwner{state} }

func (o typingOwner) Read(ctx context.Context, sub realtime.Subscription, after int64) (realtime.EphemeralFrame, error) {
	place := typing.Place{Channel: sub.Channel}
	if sub.Topic != nil {
		place.Topic = *sub.Topic
	}
	snapshot := o.state.Read(sub.Organization, place, sub.Member)
	frame := realtime.EphemeralFrame{Organization: sub.Organization, Channel: sub.Channel, Topic: sub.Topic, Generation: snapshot.Generation}
	// Acknowledge the consistently read organisation level even when another
	// place changed; otherwise the hub would wake this writer indefinitely.
	if snapshot.PlaceGeneration <= after {
		return frame, nil
	}
	var html bytes.Buffer
	if err := view.TypingIndicator(snapshot).Render(ctx, &html); err != nil {
		return frame, fmt.Errorf("rendering typing indicator: %w", err)
	}
	frame.Outgoing = realtime.Outgoing{Name: "typing", Data: html.Bytes()}
	return frame, nil
}
