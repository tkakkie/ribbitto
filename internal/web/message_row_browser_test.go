package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// At 390 x 844 a one-line message by any seed member in the default topic
// takes two rows, its header and its body, and the topic label and Select
// sit in the same place on every such row, in both languages (#1093). The
// time is fixed and so is the browser's time zone, so the local format
// (date and seconds, two-digit month, day and hour) has one width.
func testMessageRowBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// The members come from the seed's data, so a member added there is
	// checked too.
	seedFile, err := os.ReadFile("../../cmd/seed/conversations.json")
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Members []struct{ Name, Handle string }
	}
	if err := json.Unmarshal(seedFile, &seed); err != nil || len(seed.Members) == 0 {
		t.Fatalf("reading the seed members: %v (%d members)", err, len(seed.Members))
	}
	topic, cursor := kernel.ID{2}, int64(42)
	var messages []view.Message
	for i, member := range seed.Members {
		messages = append(messages, view.Message{ID: kernel.ID{byte(i + 7)}, MemberID: kernel.ID{byte(i + 20)}, TopicID: topic, EventSeq: int64(i + 7),
			DisplayName: member.Name, Handle: member.Handle, Body: "on it", DefaultTopic: true, TopicURL: view.ConversationURL("acme", kernel.ID{1}, &topic),
			CreatedAt: time.Date(2026, 12, 28, 13, 58, 58, 0, time.UTC)})
	}
	page := view.ChannelPage{Organization: view.Organization{Name: "Paper Lantern", Slug: "acme"},
		Current:     view.Channel{ID: kernel.ID{1}, DefaultTopicID: topic, Name: "general", ImageURL: view.ChannelImageURL("lake")},
		DisplayName: seed.Members[0].Name, Handle: seed.Members[0].Handle, Role: "owner", Messages: messages, EventCursor: &cursor}
	page.Channels = []view.Channel{page.Current}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("GET /organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if sender.Heartbeat(r.Context()) == nil {
			<-r.Context().Done()
		}
	})
	mux.HandleFunc("POST /organizations/acme/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/{$}", catalogues.Middleware(templ.Handler(view.ChannelScreen("/static/css/app.css", page))))
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "message rows")
	defer closeTab()
	started := time.Now()
	// step runs one named browser action and its waits under their own 10 s
	// and fails by name (#1060); nothing here goes through rod's input.
	step := func(t *testing.T, what string, fn func(p *rod.Page)) {
		t.Helper()
		stepCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		done := make(chan any, 1)
		go func() {
			defer func() { done <- recover() }()
			fn(tab.Context(stepCtx).WithPanic(func(v any) { panic(v) }))
		}()
		select {
		case v := <-done:
			if v != nil {
				t.Fatalf("%s: %v (at %s)", what, v, time.Since(started).Round(time.Millisecond))
			}
		case <-stepCtx.Done():
			t.Fatalf("%s did not finish in 10 s (at %s)", what, time.Since(started).Round(time.Millisecond))
		}
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := (proto.NetworkDeleteCookies{Name: "lang", URL: server.URL}).Call(tab.Context(cleanupCtx)); err != nil {
			t.Errorf("deleting the lang cookie: %v", err)
		}
	}()
	step(t, "setting the viewport to 390x844 and the time zone to Asia/Tokyo", func(p *rod.Page) {
		if err := (proto.EmulationSetDeviceMetricsOverride{Width: 390, Height: 844, DeviceScaleFactor: 1}).Call(p); err != nil {
			panic(err)
		}
		if err := (proto.EmulationSetTimezoneOverride{TimezoneID: "Asia/Tokyo"}).Call(p); err != nil {
			panic(err)
		}
	})
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			step(t, "loading the "+lang+" page", func(p *rod.Page) {
				if err := p.SetCookies([]*proto.NetworkCookieParam{{Name: "lang", Value: lang, URL: server.URL}}); err != nil {
					panic(err)
				}
				p.MustNavigate(server.URL + "/").MustWaitLoad()
				p.MustWait(`() => [...document.querySelectorAll('time[data-local-time]')].every(e => !e.textContent.includes('UTC'))`)
				p.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
			})
			var problems []string
			step(t, "measuring the rows", func(p *rod.Page) {
				for _, problem := range p.MustEval(messageRowAudit, len(messages)).Arr() {
					problems = append(problems, problem.Str())
				}
			})
			for _, problem := range problems {
				t.Errorf("language=%s: %s", lang, problem)
			}
		})
	}
}

// messageRowAudit returns a problem if the page does not render want message
// items, and for each message whose header is taller
// than one 44 px target row or whose item is taller than that row plus one
// body line, and for each topic label or Select box placed differently from
// the first message's, relative to its item.
const messageRowAudit = `want => {
	const problems = [], box = e => e.getBoundingClientRect(), round = n => Math.round(n * 10) / 10;
	const items = [...document.querySelectorAll('#message-items > li[data-event-seq]')];
	if (items.length !== want) return ['rendered ' + items.length + ' message items, want ' + want];
	const place = (item, e) => [round(box(e).left - box(item).left), round(box(e).top - box(item).top), round(box(e).width), round(box(e).height)];
	const first = items[0];
	for (const item of items) {
		const header = item.firstElementChild, body = item.querySelector('p[id^=body-]');
		const topic = header.querySelector('a[href]'), select = header.querySelector('label');
		const where = item.id + ' (' + JSON.stringify(header.innerText.replace(/\s+/g, ' ')) + ', clientWidth=' + document.documentElement.clientWidth + ')';
		if (box(body).height > 22.5) problems.push(where + ': the body is not one line: height=' + round(box(body).height));
		if (box(header).height > 44.5) problems.push(where + ': header takes more than one 44 px row: height=' + round(box(header).height));
		if (box(item).height > 44 + 4 + 22 + 0.5) problems.push(where + ': message takes more than two rows: height=' + round(box(item).height));
		for (const [what, e, f] of [['topic label', topic, first.querySelector('a[href]')], ['Select', select, first.querySelector('label')]]) {
			if (JSON.stringify(place(item, e)) !== JSON.stringify(place(first, f)))
				problems.push(where + ': ' + what + ' at ' + JSON.stringify(place(item, e)) + ', first message at ' + JSON.stringify(place(first, f)));
		}
	}
	return problems;
}`
