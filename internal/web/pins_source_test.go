package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type pinsReadFunc func(context.Context, org.Membership, kernel.ID) ([]conversation.Pin, error)

func (f pinsReadFunc) Pins(ctx context.Context, m org.Membership, c kernel.ID) ([]conversation.Pin, error) {
	return f(ctx, m, c)
}

func TestPinsSourceCost(t *testing.T) {
	for _, n := range []int{1, 10, 100} {
		for _, languages := range [][]string{{"en"}, {"en", "ja"}} {
			t.Run(fmt.Sprintf("%d-connections/%d-languages", n, len(languages)), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					catalogues, err := i18n.New(slog.Default())
					if err != nil {
						t.Fatal(err)
					}
					contexts := make(map[string]context.Context)
					for _, lang := range []string{"en", "ja"} {
						r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(t.Context())
						r.Header.Set("Accept-Language", lang)
						catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { contexts[lang] = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
					}

					var reads, renders atomic.Int32
					var release chan struct{}
					factory := NewPinsSource(t.Context(), pinsReadFunc(func(context.Context, org.Membership, kernel.ID) ([]conversation.Pin, error) {
						reads.Add(1)
						<-release
						return nil, nil
					}))
					m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}, Slug: "acme"}}
					shared := factory(m, kernel.ID{2}).(*pinsSource).shared
					shared.render = func(ctx context.Context, page view.ChannelPage) (realtime.Outgoing, error) {
						renders.Add(1)
						return renderPins(ctx, page)
					}
					connections := make([]realtime.ChannelStateSource, n)
					for i := range n {
						member := m
						member.Member.ID = kernel.ID{byte(i + 1)}
						connections[i] = factory(member, kernel.ID{2})
					}
					for round := 1; round <= 2; round++ {
						release = make(chan struct{})
						// One connection exercises both languages through its same adapter.
						calls := max(n, len(languages))
						done := make(chan struct{}, calls)
						for i := range calls {
							source, language := connections[i%n], languages[i%len(languages)]
							go func() {
								defer func() { done <- struct{}{} }()
								out, err := source.Refresh(contexts[language], realtime.Subscription{})
								want := i18n.T(contexts[language], "pins.empty")
								if err != nil || out.Name != "pins" || out.ID != 0 || !out.Ephemeral || !strings.Contains(string(out.Data), want) || strings.Contains(string(out.Data), "form") {
									t.Errorf("refresh = %+v, %v", out, err)
								}
							}()
						}
						synctest.Wait() // Every distinct connection has joined the blocked snapshot.
						close(release)
						for range calls {
							<-done
						}
						if reads.Load() != int32(round) || renders.Load() != int32(round*len(languages)) {
							t.Fatalf("round %d: reads=%d renders=%d", round, reads.Load(), renders.Load())
						}
					}
					t.Logf("per round: reads=1 renders=%d", len(languages))
				})
			})
		}
	}
}

func TestPinsSourceConnectDuringRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var reads atomic.Int32
		factory := NewPinsSource(t.Context(), pinsReadFunc(func(context.Context, org.Membership, kernel.ID) ([]conversation.Pin, error) {
			if reads.Add(1) == 1 {
				<-release
				return []conversation.Pin{{Excerpt: "earlier snapshot"}}, nil
			}
			return []conversation.Pin{{Excerpt: "later snapshot"}}, nil
		}))
		refresh := func(source realtime.ChannelStateSource) <-chan realtime.Outgoing {
			done := make(chan realtime.Outgoing, 1)
			go func() {
				out, err := source.Refresh(t.Context(), realtime.Subscription{})
				if err != nil {
					t.Error(err)
				}
				done <- out
			}()
			return done
		}
		m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
		older := factory(m, kernel.ID{2})
		older.Trigger(realtime.Event{OrganizationID: m.Organization.ID, ChannelID: kernel.ID{2}, Kind: conversation.KindPinsChanged, Seq: 10})
		oldDone := refresh(older)
		synctest.Wait()
		// The new connection's page already includes the change after the blocked snapshot.
		newDone := refresh(factory(m, kernel.ID{2}))
		synctest.Wait()
		if reads.Load() != 2 {
			t.Errorf("connect reused a preexisting snapshot: reads=%d, want 2", reads.Load())
		}
		close(release)
		<-oldDone
		out := <-newDone
		if !strings.Contains(string(out.Data), "later snapshot") || strings.Contains(string(out.Data), "earlier snapshot") {
			t.Errorf("connect received stale pins: %s", out.Data)
		}
	})
}

func TestPinsTriggerAndMessageFilter(t *testing.T) {
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	source := NewPinsSource(t.Context(), pinsReadFunc(func(context.Context, org.Membership, kernel.ID) ([]conversation.Pin, error) { return nil, nil }))(m, kernel.ID{2})
	for _, kind := range []realtime.EventKind{conversation.KindPinsChanged, conversation.KindMessagesMoved, conversation.KindPosted} {
		for _, scope := range []string{"same", "organisation", "channel"} {
			e := realtime.Event{OrganizationID: m.Organization.ID, ChannelID: kernel.ID{2}, Kind: kind, Topics: []kernel.ID{{99}}}
			if scope == "organisation" {
				e.OrganizationID = kernel.ID{3}
			}
			if scope == "channel" {
				e.ChannelID = kernel.ID{3}
			}
			if got, want := source.Trigger(e), scope == "same" && kind != conversation.KindPosted; got != want {
				t.Fatalf("%s/%s trigger=%t want=%t", kind, scope, got, want)
			}
		}
	}
	// Route the invalidation as a registered reader would, naming no extant message.
	e := realtime.Event{OrganizationID: m.Organization.ID, Seq: 1, Kind: conversation.KindPinsChanged, Payload: conversation.EncodePinsChanged(kernel.ID{2}, kernel.ID{99})}
	var err error
	e.ChannelID, e.Topics, err = conversationpg.PinEventKinds()[e.Kind](e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !source.Trigger(e) {
		t.Fatal("old invalidation was ignored")
	}
	out, err := source.Refresh(t.Context(), realtime.Subscription{})
	if err != nil || !strings.Contains(string(out.Data), "pins.empty") {
		t.Fatalf("vanished target: %+v, %v", out, err)
	}
	for _, interests := range [][]realtime.Interest{{realtime.InterestMessages}, {realtime.InterestSidebar}, {realtime.InterestMessages, realtime.InterestSidebar}} {
		sink := &typingDeliverySender{}
		renderer := filterWitness{messageRenderer{messages: countingMessages{calls: &atomic.Int32{}}, membership: m, renders: newRenderCache(t.Context())}}
		stream := realtime.Stream{BatchSize: 2, Hub: realtime.NewHub(), Authorizer: streamAllow(true), Renderer: renderer,
			Events: finiteMoveLog{progressLog{[]realtime.Event{e, eventOf(t, m.Organization.ID, 2, conversation.KindPosted, conversation.Moved{ChannelID: e.ChannelID, ToTopicID: kernel.ID{5}})}}, 2}}
		cursor, err := stream.Run(t.Context(), realtime.Subscription{Organization: m.Organization.ID, Channel: e.ChannelID, Interests: interests}, 0, sink)
		if cursor != 2 || !errors.Is(err, io.EOF) || len(sink.frames) != 1 {
			t.Fatalf("cursor=%d frames=%+v err=%v", cursor, sink.frames, err)
		}
		out := sink.frames[0]
		if interests[0] == realtime.InterestMessages && (out.Name != "message" || out.ID != 2 || out.Ephemeral) || interests[0] == realtime.InterestSidebar && (out.Name != "unread" || out.ID != 0 || !out.Ephemeral) {
			t.Fatalf("later frame = %+v", out)
		}
	}
}

type filterWitness struct{ messageRenderer }

func (r filterWitness) RenderUnread(ctx context.Context, e realtime.Event) (realtime.Outgoing, error) {
	if e.Kind == conversation.KindPinsChanged {
		return realtime.Outgoing{}, fmt.Errorf("pin reached unread renderer")
	}
	return r.messageRenderer.RenderUnread(ctx, e)
}

func TestPinsRefreshScopes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var reads atomic.Int32
		factory := NewPinsSource(t.Context(), pinsReadFunc(func(_ context.Context, m org.Membership, c kernel.ID) ([]conversation.Pin, error) {
			reads.Add(1)
			<-release
			return []conversation.Pin{{Excerpt: fmt.Sprintf("scope-%d-%d", m.Organization.ID[0], c[0])}}, nil
		}))
		done := make(chan struct{}, 4)
		for _, scope := range []pinsScope{{kernel.ID{1}, kernel.ID{2}}, {kernel.ID{3}, kernel.ID{2}}, {kernel.ID{1}, kernel.ID{3}}} {
			go func() {
				defer func() { done <- struct{}{} }()
				source := factory(org.Membership{Organization: org.Organization{ID: scope.organization}}, scope.channel)
				out, err := source.Refresh(t.Context(), realtime.Subscription{})
				if err != nil || !strings.Contains(string(out.Data), fmt.Sprintf("scope-%d-%d", scope.organization[0], scope.channel[0])) {
					t.Errorf("scope %v: %s, %v", scope, out.Data, err)
				}
			}()
		}
		synctest.Wait()
		source := factory(org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}, kernel.ID{2})
		source.Trigger(realtime.Event{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, Kind: conversation.KindPinsChanged, Seq: 9})
		go func() {
			defer func() { done <- struct{}{} }()
			if _, err := source.Refresh(t.Context(), realtime.Subscription{}); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if reads.Load() != 4 {
			t.Errorf("new invalidation reused an older snapshot: reads=%d", reads.Load())
		}
		close(release)
		for range 4 {
			<-done
		}
	})
}
