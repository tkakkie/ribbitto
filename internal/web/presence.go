package web

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/presence"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type presenceRenderKey struct {
	member   kernel.ID
	online   bool
	language string
}

type presenceOwner struct {
	state   *presence.State
	renders *realtime.Cache[presenceRenderKey, []byte]
}

// NewPresenceOwner shares bounded indicator renders across streams.
func NewPresenceOwner(parent context.Context, state *presence.State) realtime.EphemeralOwner {
	return presenceOwner{state, realtime.NewCache[presenceRenderKey, []byte](parent, renderCapacity, realtime.DefaultCacheLoads, renderTTL, 10*time.Second, nil, time.Now)}
}

func presenceToken(token presence.Token) string {
	return token.Process + ":" + strconv.FormatInt(token.Generation, 10)
}

func (o presenceOwner) Read(ctx context.Context, sub realtime.Subscription, after int64) (realtime.EphemeralFrame, error) {
	process, raw, _ := strings.Cut(sub.PresenceAfter, ":")
	generation, err := strconv.ParseInt(raw, 10, 64)
	start := presence.Token{}
	if err == nil && generation >= 0 {
		// The page token starts a connection; subsequent reads start at the
		// generation the writer saw, retaining the page's process identity.
		start = presence.Token{Process: process, Generation: max(generation, after)}
	}
	changes := o.state.After(sub.Organization, start)
	frame := realtime.EphemeralFrame{Organization: sub.Organization, Generation: changes.Token.Generation}
	if changes.Reset {
		frame.Outgoing.Name = "reset"
		return frame, nil
	}
	if len(changes.Entries) == 0 {
		return frame, nil
	}
	var html bytes.Buffer
	for _, entry := range changes.Entries {
		key := presenceRenderKey{entry.Member, entry.Online, i18n.Language(ctx)}
		data, err := o.renders.Get(ctx, key, func(loadCtx context.Context) ([]byte, error) {
			var indicator bytes.Buffer
			if err := view.PresenceIndicator(entry.Member, entry.Online, true).Render(loadCtx, &indicator); err != nil {
				return nil, fmt.Errorf("rendering presence indicator: %w", err)
			}
			return indicator.Bytes(), nil
		})
		if err != nil {
			return frame, err
		}
		html.Write(data)
	}
	frame.Outgoing = realtime.Outgoing{Name: "presence", Data: html.Bytes()}
	return frame, nil
}
