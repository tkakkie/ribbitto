package web

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
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
	messages   MessageReader
	membership authz.Membership
	renders    *realtime.Cache[renderKey, realtime.Outgoing]
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

func (r messageRenderer) Render(ctx context.Context, _ realtime.Subscription, event domain.Event) (realtime.Outgoing, error) {
	key := renderKey{organization: r.membership.Organization.ID, channel: event.ChannelID, seq: event.Seq, language: i18n.Language(ctx)}
	return r.renders.Get(ctx, key, func(loadCtx context.Context) (realtime.Outgoing, error) {
		entry, err := r.messages.One(loadCtx, r.membership, event.ChannelID, event.Seq)
		if err != nil {
			return realtime.Outgoing{}, err
		}
		var html bytes.Buffer
		// The load's context keeps the caller's values (the language) but
		// not its cancellation: templ stops on a cancelled context, and one
		// stream going away must not fail the render others wait for.
		if err := view.LiveMessageItem(viewMessage(entry)).Render(loadCtx, &html); err != nil {
			return realtime.Outgoing{}, fmt.Errorf("rendering message: %w", err)
		}
		return realtime.Outgoing{ID: event.Seq, Name: "message", Data: html.Bytes()}, nil
	})
}
