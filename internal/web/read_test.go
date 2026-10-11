package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type readCall func(context.Context) error

func (f readCall) Feed(ctx context.Context, _ unread.Scope, _, _ int64) error       { return f(ctx) }
func (f readCall) Topic(ctx context.Context, _ unread.TopicScope, _, _ int64) error { return f(ctx) }

type readChannels struct {
	fakeChannels
	calls atomic.Int32
}

func (c *readChannels) Get(ctx context.Context, m org.Membership, id kernel.ID) (conversation.Channel, error) {
	c.calls.Add(1)
	return c.fakeChannels.Get(ctx, m, id)
}

func TestReadAdmissionOrder(t *testing.T) {
	for _, topic := range []string{"", streamTopic} {
		t.Run(topic, func(t *testing.T) {
			channels, body := &readChannels{}, &readRecorder{}
			var lookups [][3]kernel.ID
			var reads int
			limits := middleware.NewReadRateLimiter(func() time.Time { return time.Unix(1, 0) })
			h, _ := newLimitedHandler(t, asAlice, func(s *Services) {
				s.Channels, s.Topics = channels, fakeTopics{lookups: &lookups}
				s.ReadLimits = limits
				s.Typing = typing.New(realtime.NewHub())
				s.SignalLimits = middleware.NewMemberRateLimiter(func() time.Time { return time.Unix(1, 0) })
				s.Reading = readCall(func(context.Context) error { reads++; return nil })
			})
			for i := range 5 {
				w := serveForm(h, "POST", view.ChannelURL("acme", kernel.ID{1})+"/topics/"+streamTopic+"/typing", "live", url.Values{"composer": {"bad"}})
				want := 204
				if i == 4 {
					want = 429
				}
				if w.Code != want {
					t.Fatalf("typing budget: status %d want %d", w.Code, want)
				}
			}
			path := view.ChannelURL("acme", kernel.ID{1})
			if topic != "" {
				path += "/topics/" + topic
			}
			for range 16 {
				w := serveForm(h, "POST", path+"/read", "live", url.Values{"cursor": {"1"}})
				if w.Code != 303 {
					t.Fatalf("read budget after typing: status %d", w.Code)
				}
			}
			channels.calls.Store(0)
			lookups = nil
			reads = 0
			for _, cookie := range []string{"live", ""} {
				r := httptest.NewRequest("POST", path+"/read", body)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if cookie != "" {
					r.Header.Set("Cookie", middleware.SessionCookie+"="+cookie)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := 404
				if cookie != "" {
					want = 429
				}
				if w.Code != want || (want == 429 && w.Header().Get("Retry-After") != "1") {
					t.Fatalf("status %d Retry-After %q, want %d", w.Code, w.Header().Get("Retry-After"), want)
				}
				if channels.calls.Load() != 0 || len(lookups) != 0 || body.reads != 0 || reads != 0 {
					t.Fatalf("refusal did downstream work: channel=%d topic=%d parse=%d read=%d", channels.calls.Load(), len(lookups), body.reads, reads)
				}
			}
		})
	}
}

func TestReadSlots(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancellation", "lookup", "topic", "parse"} {
		t.Run(mode, func(t *testing.T) {
			limits := middleware.NewReadRateLimiter(func() time.Time { return time.Unix(1, 0) })
			started, gate := make(chan struct{}, 4), make(chan struct{})
			var calls atomic.Int32
			p := channelPages{channels: &fakeChannels{}, topics: fakeTopics{}, readLimit: limits}
			p.reading = readCall(func(ctx context.Context) error {
				calls.Add(1)
				started <- struct{}{}
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
				if mode == "panic" {
					panic("read panic")
				}
				if mode == "error" {
					return errors.New("offline")
				}
				return nil
			})
			form := "cursor=1"
			invoke := func(ctx context.Context) (status int, panicked bool) {
				defer func() {
					if v := recover(); v != nil {
						if v != "read panic" {
							panic(v)
						}
						panicked = true
					}
				}()
				r := httptest.NewRequest("POST", "/read", strings.NewReader(form)).WithContext(ctx)
				r.SetPathValue("channelID", "01000000-0000-0000-0000-000000000000")
				r.SetPathValue("topicID", streamTopic)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("HX-Request", "true")
				w := httptest.NewRecorder()
				p.read(w, r, org.Membership{})
				return w.Code, false
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					status, panicked := invoke(ctx)
					want := 204
					if mode == "error" || mode == "cancellation" {
						want = 500
					}
					if panicked != (mode == "panic") || (!panicked && status != want) {
						t.Errorf("exit status=%d panic=%t, want %d", status, panicked, want)
					}
				})
			}
			for range 4 {
				select {
				case <-started:
				case <-t.Context().Done():
					t.Fatal("read gate not reached")
				}
			}
			refused, stop := context.WithCancel(t.Context())
			stop()
			if status, _ := invoke(refused); status != 429 || calls.Load() != 4 {
				t.Errorf("concurrency ceiling: status=%d read calls=%d", status, calls.Load())
			}
			if mode == "cancellation" {
				cancel()
			} else {
				close(gate)
			}
			wg.Wait()
			if mode == "lookup" {
				p.channels = &fakeChannels{getErr: errors.New("offline")}
				invoke(t.Context())
			}
			if mode == "topic" {
				p.channels = &fakeChannels{}
				p.topics = fakeTopics{err: conversation.ErrTopicNotFound}
				invoke(t.Context())
			}
			if mode == "parse" {
				form = "cursor=bad"
				invoke(t.Context())
			}
			var releases []func()
			for range 4 {
				release, ok := limits.Admit(kernel.ID{}, kernel.ID{})
				if !ok {
					t.Fatal("slot leaked after " + mode)
				}
				releases = append(releases, release)
			}
			for _, release := range releases {
				release()
			}
		})
	}
}
