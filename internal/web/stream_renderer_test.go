package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
)

// countingMessages is a MessageReader whose One is counted, can block and
// can fail. The body names what was read, so tests can tell renders apart.
type countingMessages struct {
	fakeMessages
	calls   *atomic.Int32
	release chan struct{}
	err     error
}

func (c countingMessages) One(_ context.Context, m authz.Membership, channel domain.ID, seq int64) (message.Entry, error) {
	c.calls.Add(1)
	if c.release != nil {
		<-c.release
	}
	if c.err != nil {
		return message.Entry{}, c.err
	}
	body := fmt.Sprintf("org %v channel %v seq %d", m.Organization.ID, channel, seq)
	return message.Entry{Message: domain.Message{ID: domain.ID{7}, Body: body}, DisplayName: "Alice", Handle: "alice"}, nil
}

// waitForRenderWaiters blocks until n renders have joined key's load.
func waitForRenderWaiters(t *testing.T, renders *realtime.Cache[renderKey, realtime.Outgoing], key renderKey, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); renders.Waiting(key) < n; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d renders joined the load", renders.Waiting(key), n)
		}
	}
}

// Streams of one organisation share each rendered message; everything that
// changes the HTML (organisation, channel, sequence, language) is in the
// key; a failed read reaches every render waiting on it and is not kept.
func TestMessageRendererSharesRenders(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	inLanguage := func(lang string) context.Context {
		var ctx context.Context
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", lang)
		catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
		return ctx
	}
	en := inLanguage("en")
	orgA, orgB := domain.ID{1}, domain.ID{3}
	memberOf := func(org domain.ID) authz.Membership {
		return authz.Membership{Organization: domain.Organization{ID: org}}
	}
	event := domain.Event{OrganizationID: orgA, Seq: 9, Kind: domain.EventMessagePosted, ChannelID: domain.ID{2}}
	keyOf := func(org domain.ID, e domain.Event) renderKey {
		return renderKey{organization: org, channel: e.ChannelID, seq: e.Seq, language: i18n.Language(en)}
	}

	t.Run("concurrent renders read once", func(t *testing.T) {
		calls := &atomic.Int32{}
		release := make(chan struct{})
		r := messageRenderer{messages: countingMessages{calls: calls, release: release}, membership: memberOf(orgA), renders: newRenderCache(t.Context())}
		const renders = 20
		var wg sync.WaitGroup
		for range renders {
			wg.Go(func() {
				out, err := r.Render(en, realtime.Subscription{}, event)
				if err != nil || out.ID != 9 || !strings.Contains(string(out.Data), "seq 9") {
					t.Errorf("Render = %+v, %v", out, err)
				}
			})
		}
		waitForRenderWaiters(t, r.renders, keyOf(orgA, event), renders)
		close(release)
		wg.Wait()
		if n := calls.Load(); n != 1 {
			t.Fatalf("%d message reads for %d concurrent renders, want 1", n, renders)
		}
	})

	t.Run("each part of the key is its own entry", func(t *testing.T) {
		calls := &atomic.Int32{}
		shared := newRenderCache(t.Context())
		render := func(ctx context.Context, org domain.ID, e domain.Event) string {
			t.Helper()
			e.OrganizationID = org
			r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(org), renders: shared}
			out, err := r.Render(ctx, realtime.Subscription{}, e)
			if err != nil {
				t.Fatal(err)
			}
			return string(out.Data)
		}
		otherChannel, otherSeq := event, event
		otherChannel.ChannelID = domain.ID{4}
		otherSeq.Seq = 10
		cases := []struct {
			name string
			ctx  context.Context
			org  domain.ID
			e    domain.Event
		}{
			{"base", en, orgA, event},
			{"organisation", en, orgB, event},
			{"channel", en, orgA, otherChannel},
			{"sequence", en, orgA, otherSeq},
			{"language", inLanguage("ja"), orgA, event},
		}
		seen := map[string]string{}
		for i, c := range cases {
			got := render(c.ctx, c.org, c.e)
			if n := calls.Load(); int(n) != i+1 {
				t.Fatalf("%s: %d reads after %d renders, want its own read", c.name, n, i+1)
			}
			// A live item carries translated announcement text, so every
			// dimension, the language included, renders different HTML.
			if other, ok := seen[got]; ok {
				t.Fatalf("%s rendered the same HTML as %s", c.name, other)
			}
			seen[got] = c.name
		}
		if want := fmt.Sprintf("org %v", orgB); !strings.Contains(render(en, orgB, event), want) {
			t.Fatalf("organisation B was served another organisation's render")
		}
	})

	t.Run("a failure reaches every waiter and is not kept", func(t *testing.T) {
		calls := &atomic.Int32{}
		release := make(chan struct{})
		failure := errors.New("database unavailable")
		shared := newRenderCache(t.Context())
		failing := messageRenderer{messages: countingMessages{calls: calls, release: release, err: failure}, membership: memberOf(orgA), renders: shared}
		const renders = 10
		var wg sync.WaitGroup
		for range renders {
			wg.Go(func() {
				if _, err := failing.Render(en, realtime.Subscription{}, event); !errors.Is(err, failure) {
					t.Errorf("Render = %v, want %v", err, failure)
				}
			})
		}
		waitForRenderWaiters(t, shared, keyOf(orgA, event), renders)
		close(release)
		wg.Wait()
		r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(orgA), renders: shared}
		if _, err := r.Render(en, realtime.Subscription{}, event); err != nil || calls.Load() != 2 {
			t.Fatalf("retry after a failure: %v after %d reads, want one fresh read", err, calls.Load())
		}
	})
}
