package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

//go:generate sh -c "../../bin/templ generate -f testdata/stream_entries.templ -stdout > message_stream_entries_test.go"

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
	t.Run("per-entry/ephemeral", func(t *testing.T) { testEphemeralSinkBrowser(t, browser) })
	for _, side := range []string{"source", "destination"} {
		for _, mode := range []string{"before", "stale", "fresh", "abort", "4xx"} {
			t.Run(side+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				source, destination := kernel.ID{1}, kernel.ID{2}
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
				move := func(from, to kernel.ID, want []int) {
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
	for _, name := range []string{"feed", "topic", "older-feed", "older-topic"} {
		t.Run(name+"/transport", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cursor, topic := int64(42), kernel.ID{2}
			snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}}, EventCursor: &cursor}
			if strings.Contains(name, "topic") {
				snapshot.Topic = &view.Topic{ID: topic}
			}
			if strings.HasPrefix(name, "older") {
				snapshot.Before = 7
			}
			var pages, streams atomic.Int32
			mux := http.NewServeMux()
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/organizations/acme/events" {
					pages.Add(1)
					templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
				if streams.Add(1) == 2 {
					want := "51"
					if snapshot.Before > 0 {
						want = "42"
					}
					if r.URL.Query().Get("after") != want {
						t.Errorf("reconnect cursor = %q, want %s", r.URL.Query().Get("after"), want)
					}
					_ = sender.Send(r.Context(), realtime.Outgoing{ID: 51, Name: "reset"})
				} else if snapshot.Before == 0 {
					_ = sender.Send(r.Context(), realtime.Outgoing{ID: 51, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 51)[0]))})
				} else {
					_ = sender.Heartbeat(r.Context())
				}
				<-r.Context().Done()
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
			defer page.MustClose()
			page.MustEvalOnNewDocument(`document.addEventListener('htmx:sseOpen', e => window.testSource = e.detail.source)`)
			page.MustNavigate(server.URL).MustWaitLoad()
			want := "51"
			if snapshot.Before > 0 {
				want = "42"
			}
			page.MustWait(`cursor => window.testSource && document.querySelector('#organization-stream').dataset.eventCursor === cursor`, want)
			// Exercise the extension's CLOSED-source retry, which reads sse-connect.
			page.MustEval(`() => { window.testSource.close(); window.testSource.dispatchEvent(new Event('error')); }`)
			for pages.Load() < 2 {
				select {
				case <-ctx.Done():
					t.Fatal("reset did not reload", ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
	t.Run("failed-move/replay", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		cursor, source, destination := int64(42), kernel.ID{1}, kernel.ID{2}
		snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
			Current: view.Channel{ID: kernel.ID{1}}, Topic: &view.Topic{ID: destination}, EventCursor: &cursor,
			Messages: streamTestMessages(destination, 40, 60)}
		move := realtime.Outgoing{ID: 51, Name: "messages-moved",
			Data: streamTestMarkup(t, view.MovedMessageItems(streamTestMessages(destination, 50), source, destination))}
		events := make(chan realtime.Outgoing, 1)
		requests := make(chan string, 2)
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/organizations/acme/events" {
				templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
				return
			}
			after := r.URL.Query().Get("after")
			requests <- after
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			if err := sender.Heartbeat(r.Context()); err != nil {
				return
			}
			select {
			case out := <-events:
				// Replay only when the requested cursor precedes the failed move.
				if after == "42" {
					_ = sender.Send(r.Context(), out)
				}
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		defer page.MustClose()
		page.MustEvalOnNewDocument(`document.addEventListener('htmx:sseOpen', e => {
			window.testSource = e.detail.source;
			e.detail.source.addEventListener('messages-moved', () => document.body.dataset.delivered = '1');
		})`)
		page.MustNavigate(server.URL).MustWaitLoad()
		page.MustWait(`() => !!window.testSource`)
		assertRequest := func() {
			t.Helper()
			select {
			case after := <-requests:
				if after != "42" {
					t.Fatalf("stream cursor = %q, want 42", after)
				}
			case <-ctx.Done():
				t.Fatal("stream did not connect", ctx.Err())
			}
		}
		assertRequest()
		page.MustEval(`() => delete document.querySelector('#load-older').dataset.oldestSeq`)
		events <- move
		page.MustWait(`() => document.body.dataset.delivered === '1'`)
		assertBrowserItems(t, page, []int{40, 60})
		if got := page.MustEval(`() => document.querySelector('#organization-stream').dataset.eventCursor`).Str(); got != "42" {
			t.Fatalf("failed move advanced cursor to %s, want 42", got)
		}
		page.MustEval(`() => {
			document.querySelector('#load-older').dataset.oldestSeq = '0';
			window.testSource.close();
			window.testSource.dispatchEvent(new Event('error'));
		}`)
		assertRequest()
		events <- move
		page.MustWait(`() => document.querySelector('#organization-stream').dataset.eventCursor === '51'`)
		assertBrowserItems(t, page, []int{40, 50, 60})
	})
}

// The frame source is test-only: no presence owner or sidebar recount is wired.
func testEphemeralSinkBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cursor, topic := int64(42), kernel.ID{2}
	snapshot := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current: view.Channel{ID: kernel.ID{1}}, EventCursor: &cursor}
	type request struct{ after, lastID string }
	requests := make(chan request, 3)
	// A nil frame ends the response to exercise native EventSource reconnect.
	frames := make(chan *realtime.Outgoing)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/acme/events" {
			templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
			return
		}
		select {
		case requests <- request{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")}:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := sender.Heartbeat(r.Context()); err != nil {
			return
		}
		for {
			select {
			case out := <-frames:
				if out == nil || sender.Send(r.Context(), *out) != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustEvalOnNewDocument(`document.addEventListener('htmx:sseOpen', e => window.testSource = e.detail.source);
		document.addEventListener('htmx:sseMessage', e => {
			if (e.target.id === 'stream-sink') {
				document.body.dataset.frame = e.detail.type;
				document.body.dataset.lastId = e.detail.lastEventId;
			}
		});`)
	page.MustNavigate(server.URL).MustWaitLoad()
	assertRequest := func(after, lastID string) {
		t.Helper()
		select {
		case got := <-requests:
			if got != (request{after, lastID}) {
				t.Fatalf("stream request = %+v, want after=%s Last-Event-ID=%s", got, after, lastID)
			}
		case <-ctx.Done():
			t.Fatal("stream did not connect", ctx.Err())
		}
	}
	send := func(out *realtime.Outgoing) {
		t.Helper()
		select {
		case frames <- out:
		case <-ctx.Done():
			t.Fatal("stream did not receive frame", ctx.Err())
		}
	}
	assertRequest("42", "")
	page.MustWait(`() => !!window.testSource`)
	if !page.MustEval(`() => {
		const sink = document.getElementById('stream-sink');
		return sink.parentElement.id === 'organization-stream' && sink.hidden &&
			sink.getAttribute('aria-hidden') === 'true' && sink.tabIndex === -1 &&
			sink.getAttribute('sse-swap') === 'sidebar,presence' && sink.getAttribute('hx-swap') === 'none';
	}`).Bool() {
		t.Fatal("sink does not meet the delivery and accessibility contract")
	}
	// Seed both browser cursors with a real durable delivery, not the page URL.
	send(&realtime.Outgoing{ID: 51, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 51)[0]))})
	page.MustWait(`() => document.getElementById('organization-stream').dataset.eventCursor === '51'`)
	for _, name := range []string{"sidebar", "presence"} {
		page.MustEval(`() => window.testEntries = ['message-help', 'message-status'].map(id => document.getElementById(id))`)
		// Put the missing target first: it must not prevent the later replacements.
		send(&realtime.Outgoing{Ephemeral: true, ID: 999, Name: name,
			Data: streamTestMarkup(t, streamBrowserEntries([]string{"stream-test-absent", "message-help", "message-status"}, name))})
		page.MustWait(`name => document.body.dataset.frame === name &&
			window.testEntries.every(old => document.getElementById(old.id) !== old &&
				document.getElementById(old.id).textContent === name)`, name)
		if !page.MustEval(`() => document.body.dataset.lastId === '51' &&
			document.getElementById('organization-stream').dataset.eventCursor === '51' &&
			new URL(document.getElementById('organization-stream').getAttribute('sse-connect'), location.href).searchParams.get('after') === '51' &&
			!document.getElementById('stream-test-absent') && !document.getElementById('stream-sink').hasChildNodes() &&
			window.testEntries.every(old => document.querySelectorAll('#' + old.id).length === 1)`).Bool() {
			t.Fatal("ephemeral delivery changed a cursor, duplicated an entry or inserted an absent target")
		}
		assertBrowserItems(t, page, []int{51})
	}
	// Native retry retains the original URL and sends its durable Last-Event-ID.
	send(nil)
	assertRequest("42", "51")
	page.MustWait(`() => window.testSource.readyState === EventSource.OPEN`)
	// The extension recreates a CLOSED source from the page's updated URL.
	page.MustEval(`() => { window.testSource.close(); window.testSource.dispatchEvent(new Event('error')); }`)
	assertRequest("51", "")
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

func streamTestMessages(topic kernel.ID, seqs ...int) []view.Message {
	var messages []view.Message
	for _, seq := range seqs {
		messages = append(messages, view.Message{ID: kernel.ID{byte(seq)}, TopicID: topic, EventSeq: int64(seq), Body: fmt.Sprint(seq)})
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
		ids = append(ids, view.MessageDOMID(kernel.ID{byte(seq)}))
	}
	got := page.MustEval(`() => Array.from(document.querySelector('#message-items').children, e => e.id).join(',')`).Str()
	if want := strings.Join(ids, ","); got != want {
		t.Fatalf("items = %s, want %s (sequences %v)", got, want, seqs)
	}
}
