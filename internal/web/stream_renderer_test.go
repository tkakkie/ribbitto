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

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// countingMessages is the renderer's reader, whose One is counted, can
// block and can fail. The body names what was read, so tests can tell
// renders apart.
type countingMessages struct {
	calls   *atomic.Int32
	release chan struct{}
	err     error
}

func (c countingMessages) One(_ context.Context, m org.Membership, channel kernel.ID, seq int64) (conversation.Entry, error) {
	c.calls.Add(1)
	if c.release != nil {
		<-c.release
	}
	if c.err != nil {
		return conversation.Entry{}, c.err
	}
	body := fmt.Sprintf("org %v channel %v seq %d", m.Organization.ID, channel, seq)
	return conversation.Entry{Message: conversation.Message{ID: kernel.ID{7}, TopicID: kernel.ID{6}, EventSeq: seq, Body: body}, DisplayName: "Alice", Handle: "alice"}, nil
}

func (c countingMessages) Many(ctx context.Context, m org.Membership, channel kernel.ID, ids []kernel.ID) ([]conversation.Entry, error) {
	entry, err := c.One(ctx, m, channel, 9)
	entries := make([]conversation.Entry, 0, len(ids))
	for _, id := range ids {
		entry.ID = id
		entries = append(entries, entry)
	}
	return entries, err
}

// eventOf builds kind's event as the reader delivers it: the payload from
// its publisher's codec, and the channel and routing topics its Router gives.
// A post is moved.ToTopicID's; a move carries moved itself.
func eventOf(t *testing.T, org kernel.ID, seq int64, kind realtime.EventKind, moved conversation.Moved) realtime.Event {
	t.Helper()
	e := realtime.Event{OrganizationID: org, Seq: seq, Kind: kind}
	var route realtime.Router
	switch kind {
	case conversation.KindPosted:
		e.Payload = conversation.EncodePosted(moved.ChannelID, kernel.ID{7}, moved.ToTopicID)
		route = conversation.RoutePosted
	case conversation.KindMessagesMoved:
		e.Payload = conversation.EncodeMoved(moved)
		route = conversation.RouteMoved
	default:
		t.Fatalf("no codec for %q", kind)
	}
	var err error
	if e.ChannelID, e.Topics, err = route(e.Payload); err != nil {
		t.Fatal(err)
	}
	return e
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
	orgA, orgB := kernel.ID{1}, kernel.ID{3}
	memberOf := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}}
	}
	base := conversation.Moved{ChannelID: kernel.ID{2}, FromTopicID: kernel.ID{1}, ToTopicID: kernel.ID{2}}
	event := eventOf(t, orgA, 9, conversation.KindPosted, base)
	keyOf := func(org kernel.ID, e realtime.Event) renderKey {
		return renderKey{organization: org, channel: e.ChannelID, seq: e.Seq, language: i18n.Language(en)}
	}

	for _, kind := range []realtime.EventKind{conversation.KindPosted, conversation.KindMessagesMoved} {
		t.Run("concurrent renders read once/"+string(kind), func(t *testing.T) {
			moved := base
			for i := range 100 {
				moved.MessageIDs = append(moved.MessageIDs, kernel.ID{byte(i)})
			}
			event := eventOf(t, orgA, 9, kind, moved)
			calls := &atomic.Int32{}
			release := make(chan struct{})
			r := messageRenderer{messages: countingMessages{calls: calls, release: release}, membership: memberOf(orgA), renders: newRenderCache(t.Context())}
			const renders = 20
			var wg sync.WaitGroup
			for range renders {
				wg.Go(func() {
					out, err := r.Render(en, event)
					if err != nil || out.ID != 9 || !strings.Contains(string(out.Data), "seq 9") || (kind == conversation.KindMessagesMoved && strings.Count(string(out.Data), "<li ") != 100) {
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
	}
	t.Run("each part of the key is its own entry", func(t *testing.T) {
		calls := &atomic.Int32{}
		shared := newRenderCache(t.Context())
		render := func(ctx context.Context, org kernel.ID, e realtime.Event) string {
			t.Helper()
			e.OrganizationID = org
			r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(org), renders: shared}
			out, err := r.Render(ctx, e)
			if err != nil {
				t.Fatal(err)
			}
			return string(out.Data)
		}
		inOtherChannel := base
		inOtherChannel.ChannelID = kernel.ID{4}
		otherChannel, otherSeq := eventOf(t, orgA, 9, conversation.KindPosted, inOtherChannel), eventOf(t, orgA, 10, conversation.KindPosted, base)
		cases := []struct {
			name string
			ctx  context.Context
			org  kernel.ID
			e    realtime.Event
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

	// The data attributes of the DOM contract (#351) on both stream payloads.
	t.Run("payloads carry the contract's data attributes", func(t *testing.T) {
		r := messageRenderer{messages: countingMessages{calls: &atomic.Int32{}}, membership: memberOf(orgA), renders: newRenderCache(t.Context())}
		const topicSix = "06000000-0000-0000-0000-000000000000"
		moved := conversation.Moved{ChannelID: event.ChannelID, FromTopicID: kernel.ID{5}, ToTopicID: kernel.ID{6}, MessageIDs: []kernel.ID{{7}, {8}}}
		for _, kind := range []realtime.EventKind{conversation.KindPosted, conversation.KindMessagesMoved} {
			seq := int64(9)
			if kind == conversation.KindMessagesMoved {
				seq = 11 // its own render, not the posted one's
			}
			out, err := r.Render(en, eventOf(t, orgA, seq, kind, moved))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(strings.NewReader(string(out.Data)))
			if err != nil {
				t.Fatal(err)
			}
			if kind == conversation.KindMessagesMoved {
				list := find(doc, atom.Ul)
				if list == nil || attr(list, "data-from-topic") != "05000000-0000-0000-0000-000000000000" || attr(list, "data-to-topic") != topicSix {
					t.Errorf("%s: the payload's list lost its move routing", kind)
				}
			}
			items := 0
			for n := range doc.Descendants() {
				if n.DataAtom == atom.Li {
					items++
					if got, ok := attrOK(n, "data-event-seq"); !ok || got != "9" {
						t.Errorf("%s: data-event-seq = %q, %t; want 9", kind, got, ok)
					}
				}
				if n.DataAtom == atom.Input && attr(n, "type") == "checkbox" && attr(n, "data-source") != topicSix {
					t.Errorf("%s: data-source = %q; want the message's topic", kind, attr(n, "data-source"))
				}
			}
			if want := len(moved.MessageIDs); kind == conversation.KindPosted && items != 1 || kind == conversation.KindMessagesMoved && items != want {
				t.Errorf("%s: %d items", kind, items)
			}
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
				if _, err := failing.Render(en, event); !errors.Is(err, failure) {
					t.Errorf("Render = %v, want %v", err, failure)
				}
			})
		}
		waitForRenderWaiters(t, shared, keyOf(orgA, event), renders)
		close(release)
		wg.Wait()
		r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(orgA), renders: shared}
		if _, err := r.Render(en, event); err != nil || calls.Load() != 2 {
			t.Fatalf("retry after a failure: %v after %d reads, want one fresh read", err, calls.Load())
		}
	})

	// A kind without a live render fails, naming the kind, before any read
	// or cache entry; it never falls through to the posted render.
	t.Run("a kind without a render is an error naming it", func(t *testing.T) {
		calls := &atomic.Int32{}
		r := messageRenderer{messages: countingMessages{calls: calls}, membership: memberOf(orgA), renders: newRenderCache(t.Context())}
		unknown := event
		unknown.Kind = "test.unknown"
		_, err := r.Render(en, unknown)
		if err == nil || !strings.Contains(err.Error(), `"test.unknown"`) || calls.Load() != 0 {
			t.Fatalf("Render = %v after %d reads; want an error naming the kind and no read", err, calls.Load())
		}
		if out, err := r.Render(en, event); err != nil || out.Name != "message" {
			t.Fatalf("the posted kind after it = %+v, %v", out, err)
		}
	})
}
