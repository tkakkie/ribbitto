package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

type progressLog struct{ events []realtime.Event }

func (l progressLog) EventsAfter(_ context.Context, _ kernel.ID, after int64, _ int) ([]realtime.Event, error) {
	var events []realtime.Event
	for _, e := range l.events {
		if e.Seq > after {
			events = append(events, e)
		}
	}
	return events, nil
}

func testStreamProgressBrowser(t *testing.T, browser *rod.Browser) {
	for _, scope := range []string{"sidebar", "quiet channel"} {
		for _, reconnect := range []string{"native", "htmx"} {
			t.Run(scope+"/"+reconnect, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cursor, organization, channel, topic := int64(42), kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
				snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: channel}, EventCursor: &cursor}
				if scope == "sidebar" {
					snapshot.Before = 7
				}
				events := progressLog{[]realtime.Event{
					{OrganizationID: organization, ChannelID: kernel.ID{4}, Seq: 43, Kind: conversation.KindPosted,
						Payload: conversation.EncodePosted(kernel.ID{4}, kernel.ID{6}, topic, kernel.ID{5})},
					{OrganizationID: organization, Seq: 44}, // Unrouted events are skipped.
				}}
				requests, disconnect := make(chan string, 4), make(chan struct{})
				mux := http.NewServeMux()
				mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
				mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/organizations/acme/events" {
						templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
						return
					}
					after, ok := streamCursor(r)
					if !ok {
						t.Error("invalid resume cursor")
						return
					}
					requests <- fmt.Sprintf("%s/%s", r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID"))
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "retry: 0\n\n")
					sender := &sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
					sub := realtime.Subscription{Organization: organization, Channel: channel, Interests: []realtime.Interest{realtime.InterestSidebar}}
					if scope != "sidebar" {
						sub.Interests = append(sub.Interests, realtime.InterestMessages)
					}
					// No heartbeat in this fixture: checkpoint before waiting. Production
					// may replay about one heartbeat interval before receipt; only
					// checkpoint-covered events must disappear from reconnect replay.
					hub := realtime.NewHub()
					hub.Raise(organization, 45)
					stream := realtime.Stream{BatchSize: 3, Hub: hub, Events: finiteMoveLog{events, 44}, Authorizer: streamAllow(true), Renderer: messageRenderer{renders: newRenderCache(ctx)}}
					_, _ = stream.Run(r.Context(), sub, after, sender)
					_ = sender.Send(r.Context(), realtime.Outgoing{Ephemeral: true, Name: "replay-complete"})
					select {
					case <-disconnect:
					case <-r.Context().Done():
					}
				})
				server := httptest.NewServer(mux)
				defer server.Close()
				page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
				defer page.MustClose()
				page.MustEvalOnNewDocument(`window.unreads=0; window.ready=0; document.addEventListener('htmx:sseOpen', e => {
     window.testSource=e.detail.source; e.detail.source.addEventListener('unread', () => window.unreads++);
     e.detail.source.addEventListener('replay-complete', () => window.ready++, {once:true});
    });`)
				page.MustNavigate(server.URL).MustWaitLoad()
				request := func(want string) {
					t.Helper()
					select {
					case got := <-requests:
						if got != want {
							t.Fatalf("resume request=%s, want %s", got, want)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				request("42/")
				page.MustWait(`() => window.ready === 1 && new URL(document.getElementById('organization-stream').getAttribute('sse-connect'), location.href).searchParams.get('after') === '44'`)
				if !page.MustEval(`() => window.unreads === 1 && document.getElementById('organization-stream').dataset.eventCursor === '42' && !document.getElementById('organization-stream').dataset.appliedCursor`).Bool() {
					t.Fatal("unread/checkpoint changed applied cursor or lost unread delivery")
				}
				if reconnect == "native" {
					disconnect <- struct{}{}
					request("42/44")
				} else {
					page.MustEval(`() => { window.testSource.close(); window.testSource.dispatchEvent(new Event('error')); }`)
					request("44/")
				}
				page.MustWait(`() => window.ready === 2`)
				if page.MustEval(`() => window.unreads`).Int() != 1 {
					t.Fatal("checkpoint-covered unread replayed after reconnect")
				}
			})
		}
	}
}
