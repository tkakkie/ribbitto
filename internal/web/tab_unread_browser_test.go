package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testTabUnreadBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	channel, _ := pathID("00000000-0000-0000-0000-000000000001")
	topic, _ := pathID("00000000-0000-0000-0000-000000000002")
	cursor := int64(42)
	snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme", Name: "Acme"},
		Current: view.Channel{ID: channel, Name: "general"}, Channels: []view.Channel{{ID: channel, Name: "general"}},
		Topics: []view.Topic{{ID: topic, Name: "topic"}}, MemberID: kernel.ID{8}, EventCursor: &cursor, SnapshotSeq: cursor}
	snapshot.Older = true
	snapshot.Messages = feedFixture(t, 1).Messages
	frames := make(chan realtime.Outgoing)
	replacements := make(chan []byte, 1)
	connected := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if sender.Heartbeat(r.Context()) != nil {
			return
		}
		select {
		case connected <- struct{}{}:
		default:
		}
		for {
			select {
			case frame := <-frames:
				if sender.Send(r.Context(), frame) != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resync") {
			var data []byte
			select {
			case data = <-replacements:
			case <-r.Context().Done():
				return
			}
			select {
			case frames <- realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: data}:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		current := snapshot
		if r.URL.Query().Has("before") {
			current.Older = false
			current.Before = 1
			current.Messages = nil
		}
		templ.Handler(view.ChannelScreen("/static/css/app.css", current)).ServeHTTP(w, r)
	})
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(middleware.SecurityHeaders(catalogues.Middleware(mux)))
	defer server.Close()
	page, closePage := boundedPage(t, browser, ctx, "tab unread")
	defer closePage()
	page.MustEvalOnNewDocument(`window.violations=[]; window.errors=[]; addEventListener('error', e => errors.push(e.message)); document.addEventListener('htmx:sseOpen', () => window.opened=true); document.addEventListener('securitypolicyviolation', e => violations.push(e.violatedDirective));`)
	page.MustNavigate(server.URL).MustWaitLoad()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	page.MustWait(`() => window.opened === true`)
	title := page.MustEval(`() => document.title`).Str()
	plain := page.MustEval(`() => document.querySelector('link[rel="icon"]').href`).Str()
	send := func(frame realtime.Outgoing) {
		t.Helper()
		select {
		case frames <- frame:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	send(realtime.Outgoing{Name: "unread", Ephemeral: true, Data: []byte(`{"channel":"00000000-0000-0000-0000-000000000001","topic":"00000000-0000-0000-0000-000000000002","author":"00000000-0000-0000-0000-000000000003","seq":43}`)})
	if err := page.Timeout(5 * time.Second).Wait(rod.Eval(`title => document.title === '(1) '+title && document.querySelector('link[rel="icon"]').href.startsWith('data:image/png')`, title)); err != nil {
		t.Fatalf("live update: %v; %s", err, page.MustEval(`() => JSON.stringify({title:document.title, badged:document.querySelector('link[rel="icon"]').href.startsWith('data:'), counts:[...document.querySelectorAll('[data-unread-count]')].map(n=>n.getAttribute('data-unread-count')), errors, violations})`).Str())
	}
	for _, scheme := range []string{"light", "dark"} {
		if err := (proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-color-scheme", Value: scheme}}}).Call(page); err != nil {
			t.Fatal(err)
		}
		// Decode the actual 16px favicon under the page CSP and inspect the dot
		// centre and ring against the same resolved page tokens.
		if err := page.Timeout(3 * time.Second).Wait(rod.Eval(`async () => {
   const image = new Image(); image.src = document.querySelector('link[rel="icon"]').href; await image.decode();
   const canvas = document.createElement('canvas'); canvas.width=canvas.height=16;
   const ctx=canvas.getContext('2d'); ctx.drawImage(image,0,0);
   const center=[...ctx.getImageData(12,12,1,1).data];
   const ring=[...ctx.getImageData(14,12,1,1).data];
   const colors=getComputedStyle(document.documentElement);
   ctx.fillStyle=colors.getPropertyValue('--rb-brand'); ctx.fillRect(0,0,1,1);
   const brand=[...ctx.getImageData(0,0,1,1).data];
   ctx.fillStyle=colors.getPropertyValue('--rb-on-brand'); ctx.fillRect(0,0,1,1);
   const outline=[...ctx.getImageData(0,0,1,1).data];
   window.pixels={center,ring,brand,outline};
   return image.naturalWidth===16 && center.join()===brand.join() && ring.slice(0,3).join()===outline.slice(0,3).join();
  }`).ByPromise()); err != nil {
			t.Fatalf("%s dot pixels: %v; %s", scheme, err, page.MustEval(`() => JSON.stringify(pixels)`).Str())
		}
	}
	page.MustElement("#load-older a").MustClick()
	page.MustWait(`() => !document.querySelector('#load-older a')`)
	if err := page.Timeout(3 * time.Second).Wait(rod.Eval(`title => document.title === '(1) '+title && document.querySelector('link[rel="icon"]').href.startsWith('data:image/png')`, title)); err != nil {
		t.Fatalf("older history title/icon: %v; title=%q", err, page.MustEval(`() => document.title`).Str())
	}
	for i, count := range []int64{100, 0} {
		replacement := snapshot
		seq := int64(44 + i)
		replacement.SnapshotSeq = seq
		replacement.ChannelCounts = map[kernel.ID]int64{channel: count}
		replacement.TopicCounts = map[kernel.ID]int64{topic: 999}
		replacements <- []byte(renderIn(t, "en", view.SidebarLists(replacement)))
		page.MustEval(`() => { window.oldChannels=document.querySelector('#sidebar-channels'); htmx.trigger(document.querySelector('#sidebar-resync'),'sidebar-resync'); }`)
		want := title
		if count > 0 {
			want = "(99+) " + title
		}
		page.MustWait(`(title, plain, badge) => document.querySelector('#sidebar-channels')!==oldChannels && document.title===title && (badge ? document.querySelector('link[rel="icon"]').href.startsWith('data:image/png') : document.querySelector('link[rel="icon"]').href===plain)`, want, plain, count > 0)
	}
	if got := page.MustEval(`() => violations.join(',')`).Str(); got != "" {
		t.Fatalf("CSP violations: %s", got)
	}
	// The response really uses the production policy, including nonce-only scripts.
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	policy := response.Header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "img-src 'self' data:") || !strings.Contains(policy, "script-src 'nonce-") || strings.Contains(policy, "script-src 'self'") {
		t.Fatalf("unexpected CSP: %s", policy)
	}
	if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(server.URL).MustWaitLoad()
	if got := page.MustEval(`() => document.title+'|'+document.querySelector('link[rel="icon"]').href`).Str(); got != fmt.Sprint(title, "|", plain) {
		t.Fatalf("no-JS title/icon: %s", got)
	}
}
