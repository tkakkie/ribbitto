package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func TestMessageStreamBrowser(t *testing.T) {
	bin, found := launcher.LookPath()
	if !found {
		browserUnavailable(t, "no installed browser")
	}
	// This bounds only the launch. In CI a browser has missed a 10-second
	// limit before the scenarios ran (#415); one that misses this still fails.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Leakless(false).Context(ctx)
	url, err := launch.Launch()
	if err != nil {
		browserUnavailable(t, err.Error())
	}
	t.Cleanup(launch.Kill)
	browser := rod.New().ControlURL(url).MustConnect()
	t.Cleanup(func() { browser.Timeout(5 * time.Second).MustClose() })
	for _, side := range []string{"source", "destination"} {
		for _, mode := range []string{"before", "stale", "fresh", "abort", "4xx"} {
			t.Run(side+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				source, destination := domain.ID{1}, domain.ID{2}
				selected, initial, afterMove := destination, []int{40, 60}, []int{40, 50, 60}
				stale, fresh := []int{20, 35}, []int{20, 30, 35}
				if side == "source" {
					selected, initial, afterMove = source, []int{50, 61}, []int{61}
					stale, fresh = []int{20, 30, 35}, []int{20, 35}
				}
				snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
					Topic: &view.Topic{ID: selected}, EventCursor: new(int64), Older: true}
				snapshot.Messages = streamTestMessages(selected, initial...)
				events := make(chan realtime.Outgoing, 1)
				requests := make(chan browserHistory)
				mux := http.NewServeMux()
				mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
				mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/events") {
						w.Header().Set("Content-Type", "text/event-stream")
						sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
						if err := sender.Heartbeat(r.Context()); err != nil {
							return
						}
						for {
							select {
							case out := <-events:
								if err := sender.Send(r.Context(), out); err != nil {
									return
								}
							case <-r.Context().Done():
								return
							}
						}
					}
					if before := r.URL.Query().Get("before"); before != "" {
						request := browserHistory{before: before, reply: make(chan browserReply, 1)}
						select {
						case requests <- request:
						case <-r.Context().Done():
							return
						}
						select {
						case reply := <-request.reply:
							w.Header().Set("Content-Type", "text/html; charset=utf-8")
							w.WriteHeader(reply.status)
							_, _ = w.Write(reply.body)
						case <-r.Context().Done():
						}
						return
					}
					templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
				})
				server := httptest.NewServer(mux)
				defer server.Close()
				page := browser.MustPage().WithPanic(func(v any) { t.Fatal(v) })
				defer page.MustClose()
				page = page.Context(ctx)
				page.MustNavigate(server.URL).MustWaitLoad()
				// Observe completed script deliveries and real XHR lifecycle events,
				// without replacing EventSource, htmx or the application callbacks.
				page.MustEval(`() => {
					document.body.dataset.delivered = 0;
					document.body.dataset.completed = 0;
					document.addEventListener('htmx:sseBeforeMessage', () => document.body.dataset.delivered++);
					document.addEventListener('htmx:afterRequest', e => {
						if (e.detail.target?.id === 'message-items') document.body.dataset.completed++;
					});
				}`)
				deliveries, completed := 0, 0
				move := func(from, to domain.ID, want []int) {
					t.Helper()
					out := realtime.Outgoing{ID: 100 + int64(deliveries), Name: "messages-moved",
						Data: streamTestMarkup(t, view.MovedMessageItems(streamTestMessages(to, 10, 30, 50), from, to))}
					for range 2 { // Identical event IDs and payloads exercise duplicate replay.
						events <- out
						deliveries++
						page.MustWait(`n => +document.body.dataset.delivered === n`, deliveries)
						assertBrowserItems(t, page, want)
					}
				}
				begin := func(bound int) browserHistory {
					t.Helper()
					page.MustEval(`() => document.querySelector('#load-older a').click()`)
					select {
					case request := <-requests:
						if request.before != fmt.Sprint(bound) {
							t.Fatalf("history before=%s, want %d", request.before, bound)
						}
						return request
					case <-ctx.Done():
						t.Fatal(ctx.Err())
						return browserHistory{}
					}
				}
				finish := func() {
					completed++
					page.MustWait(`n => +document.body.dataset.completed === n && !document.querySelector('.htmx-settling')`, completed)
				}
				history := func(ids []int, older bool) []byte {
					response := snapshot
					response.Before, response.EventCursor, response.Older = 40, nil, older
					response.Messages = streamTestMessages(selected, ids...)
					return streamTestMarkup(t, view.ChannelScreen("/static/css/app.css", response))
				}
				assertBrowserItems(t, page, initial)
				if mode == "before" {
					move(source, destination, afterMove)
				}
				request := begin(initial[0])
				if mode != "before" {
					move(source, destination, afterMove)
				}
				response, expectedOlder := fresh, fresh
				if mode == "stale" {
					response = stale
				}
				if mode == "abort" || mode == "4xx" {
					if mode == "abort" {
						page.MustEval(`() => htmx.trigger(document.querySelector('#load-older a'), 'htmx:abort')`)
					} else {
						request.reply <- browserReply{status: http.StatusBadRequest}
					}
					finish()
					assertBrowserItems(t, page, afterMove)
					// Undo before the next request. Replaying the failed request's
					// old move would now corrupt both pages' next history slice.
					move(destination, source, initial)
					request = begin(initial[0])
					response, expectedOlder, afterMove = stale, stale, initial
				}
				request.reply <- browserReply{status: http.StatusOK, body: history(response, true)}
				finish()
				want := append(append([]int{}, expectedOlder...), afterMove...)
				assertBrowserItems(t, page, want)
				// Exhaust history, including the moved ID below both previous bounds.
				oldest := []int{5}
				if (side == "destination") == (mode != "abort" && mode != "4xx") {
					oldest = []int{5, 10}
				}
				request = begin(20)
				request.reply <- browserReply{status: http.StatusOK, body: history(oldest, false)}
				finish()
				assertBrowserItems(t, page, append(oldest, want...))
				page.MustWait(`() => document.querySelector('#load-older').dataset.oldestSeq === '0'`)
			})
		}
	}
}

func browserUnavailable(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("RIBBITTO_REQUIRE_BROWSER") == "1" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

type browserHistory struct {
	before string
	reply  chan browserReply
}

type browserReply struct {
	status int
	body   []byte
}

func streamTestMessages(topic domain.ID, seqs ...int) []view.Message {
	var messages []view.Message
	for _, seq := range seqs {
		messages = append(messages, view.Message{ID: domain.ID{byte(seq)}, TopicID: topic, EventSeq: int64(seq), Body: fmt.Sprint(seq)})
	}
	return messages
}

func streamTestMarkup(t *testing.T, component templ.Component) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := component.Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func assertBrowserItems(t *testing.T, page *rod.Page, seqs []int) {
	t.Helper()
	var ids []string
	for _, seq := range seqs {
		ids = append(ids, view.MessageDOMID(domain.ID{byte(seq)}))
	}
	got := page.MustEval(`() => Array.from(document.querySelector('#message-items').children, e => e.id).join(',')`).Str()
	if want := strings.Join(ids, ","); got != want {
		t.Fatalf("items = %s, want %s (sequences %v)", got, want, seqs)
	}
}
