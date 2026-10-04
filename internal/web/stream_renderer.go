package web

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// messageRenderer shares the page's message markup, adding announcement
// text only for live delivery. It reads
// with the membership resolved when the stream opened; whether the member
// may still see the event is the Authorizer's decision, made just before.
//
// Renders are shared between an organisation's streams through renders
// (#227): the output depends only on the message and the language, so
// streams of different members share it, and each language has its own.
type messageRenderer struct {
	messages   liveMessages
	membership org.Membership
	renders    *realtime.Cache[renderKey, realtime.Outgoing]
}

// liveMessages is what the renderer reads: one message by sequence for a
// post, and a move's batch by ID. The channel page's MessageReader provides
// both.
type liveMessages interface {
	One(context.Context, org.Membership, domain.ID, int64) (message.Entry, error)
	Many(context.Context, org.Membership, domain.ID, []domain.ID) ([]message.Entry, error)
}

type renderKey struct {
	organization, channel domain.ID
	seq                   int64
	language              string
}

// Render caching bounds: names shown in a live message can be up to
// renderTTL old (the page always reads them fresh).
const (
	renderCapacity = 4096
	renderTTL      = time.Minute
)

func newRenderCache(parent context.Context) *realtime.Cache[renderKey, realtime.Outgoing] {
	return realtime.NewCache[renderKey, realtime.Outgoing](parent, renderCapacity, realtime.DefaultCacheLoads, renderTTL, 10*time.Second, nil, time.Now)
}

// Render renders event through its kind's function. A kind without one is
// an error naming it, so a new channel-scoped kind fails visibly instead of
// rendering as a post.
func (r messageRenderer) Render(ctx context.Context, event realtime.Event) (realtime.Outgoing, error) {
	var render func(context.Context, realtime.Event) (realtime.Outgoing, error)
	switch event.Kind {
	case conversation.KindPosted:
		render = r.renderPosted
	case conversation.KindMessagesMoved:
		render = r.renderMoved
	default:
		return realtime.Outgoing{}, fmt.Errorf("no live render for event kind %q", event.Kind)
	}
	key := renderKey{organization: r.membership.Organization.ID, channel: event.ChannelID, seq: event.Seq, language: i18n.Language(ctx)}
	return r.renders.Get(ctx, key, func(loadCtx context.Context) (realtime.Outgoing, error) {
		return render(loadCtx, event)
	})
}

// renderPosted renders a posted message. ctx is the cache load's context:
// it keeps the caller's values (the language) but not its cancellation,
// because templ stops on a cancelled context and one stream going away must
// not fail the render others wait for.
func (r messageRenderer) renderPosted(ctx context.Context, event realtime.Event) (realtime.Outgoing, error) {
	entry, err := r.messages.One(ctx, r.membership, event.ChannelID, event.Seq)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	var html bytes.Buffer
	if err := view.LiveMessageItem(viewMessage(r.membership.Organization.Slug, entry)).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering message: %w", err)
	}
	return realtime.Outgoing{ID: event.Seq, Name: "message", Data: html.Bytes(), Topic: entry.TopicID}, nil
}

// renderMoved renders the messages a branch moved, with the same load
// context as renderPosted.
func (r messageRenderer) renderMoved(ctx context.Context, event realtime.Event) (realtime.Outgoing, error) {
	moved, err := conversation.DecodeMoved(event.Payload)
	if err != nil {
		return realtime.Outgoing{}, fmt.Errorf("decoding moved messages: %w", err)
	}
	entries, err := r.messages.Many(ctx, r.membership, event.ChannelID, moved.MessageIDs)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	items := make([]view.Message, 0, len(entries))
	for _, entry := range entries {
		items = append(items, viewMessage(r.membership.Organization.Slug, entry))
	}
	var html bytes.Buffer
	if err := view.MovedMessageItems(items, moved.FromTopicID, moved.ToTopicID).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering moved messages: %w", err)
	}
	return realtime.Outgoing{ID: event.Seq, Name: "messages-moved", Data: html.Bytes()}, nil
}
