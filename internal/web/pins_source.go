package web

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type pinsReader interface {
	Pins(context.Context, org.Membership, kernel.ID) ([]conversation.Pin, error)
}

type pinsScope struct{ organization, channel kernel.ID }
type pinsRound struct {
	users   int
	trigger int64
	pins    *realtime.Cache[bool, []conversation.Pin]
	renders *realtime.Cache[string, realtime.Outgoing]
}

type pinsSource struct {
	shared     *pinsRefreshes
	membership org.Membership
	channel    kernel.ID
	trigger    int64
}

type pinsRefreshes struct {
	parent context.Context
	reader pinsReader
	render func(context.Context, view.ChannelPage) (realtime.Outgoing, error)
	mu     sync.Mutex
	rounds map[pinsScope]*pinsRound
}

// NewPinsSource binds membership and channel; Stream authorizes each send.
// Overlapping Refresh calls share a round unless a newer invalidation intervenes.
// The last caller discards its round, so completed snapshots are never reused.
func NewPinsSource(parent context.Context, reader pinsReader) func(org.Membership, kernel.ID) realtime.ChannelStateSource {
	shared := &pinsRefreshes{parent: parent, reader: reader, render: renderPins, rounds: make(map[pinsScope]*pinsRound)}
	return func(m org.Membership, channel kernel.ID) realtime.ChannelStateSource {
		return &pinsSource{shared: shared, membership: m, channel: channel}
	}
}

func (s *pinsSource) Trigger(event realtime.Event) bool {
	matches := event.OrganizationID == s.membership.Organization.ID && event.ChannelID == s.channel &&
		(event.Kind == conversation.KindPinsChanged || event.Kind == conversation.KindMessagesMoved)
	if matches {
		s.trigger = max(s.trigger, event.Seq)
	}
	return matches
}

func (s *pinsSource) Refresh(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	key := pinsScope{s.membership.Organization.ID, s.channel}
	shared := s.shared
	shared.mu.Lock()
	round := shared.rounds[key]
	// A snapshot begun before this invalidation cannot answer its refresh.
	if round == nil || round.trigger < s.trigger {
		// The caches live only for this round, never for an event sequence.
		// A frozen clock prevents expiry during an overlapping refresh.
		at := time.Now()
		now := func() time.Time { return at }
		round = &pinsRound{trigger: s.trigger,
			pins:    realtime.NewCache[bool, []conversation.Pin](shared.parent, 1, 1, time.Second, 10*time.Second, nil, now),
			renders: realtime.NewCache[string, realtime.Outgoing](shared.parent, 2, 2, time.Second, 10*time.Second, nil, now)}
		shared.rounds[key] = round
	}
	round.users++
	shared.mu.Unlock()
	defer func() {
		shared.mu.Lock()
		defer shared.mu.Unlock()
		round.users--
		if round.users == 0 && shared.rounds[key] == round {
			delete(shared.rounds, key)
		}
	}()
	pins, err := round.pins.Get(ctx, true, func(ctx context.Context) ([]conversation.Pin, error) {
		return shared.reader.Pins(ctx, s.membership, s.channel)
	})
	if err != nil {
		return realtime.Outgoing{}, err
	}
	return round.renders.Get(ctx, i18n.Language(ctx), func(ctx context.Context) (realtime.Outgoing, error) {
		page := view.ChannelPage{Organization: view.Organization{Slug: s.membership.Organization.Slug}, Pins: viewPins(pins)}
		return shared.render(ctx, page)
	})
}

func renderPins(ctx context.Context, page view.ChannelPage) (realtime.Outgoing, error) {
	var html bytes.Buffer
	if err := view.PinsSection(page).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering pins: %w", err)
	}
	return realtime.Outgoing{Name: "pins", Ephemeral: true, Data: html.Bytes()}, nil
}
