package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestTypingRefusals(t *testing.T) {
	for _, mode := range []string{"signed out", "non-member", "csrf", "channel", "topic", "channel lookup", "topic lookup", "missing token", "malformed token", "duplicate token", "limited", "past deadline", "past cleanup"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := typing.New(realtime.NewHub())
				place := typing.Place{Channel: kernel.ID{1}, Topic: kernel.ID{2}}
				defer state.Open(kernel.ID{}, kernel.ID{}, place.Channel)()
				token := state.IssueComposer(kernel.ID{}, place, kernel.ID{})
				state.Signal(kernel.ID{}, place, typing.Typist{Handle: "alice"}, token, time.Now())
				before := state.Read(kernel.ID{}, place, kernel.ID{9})
				h, _ := newLimitedHandler(t, asAlice, func(s *Services) {
					s.Typing = state
					if mode == "non-member" {
						s.Authz = noOrganisations{}
					}
					if mode == "channel lookup" {
						s.Channels = &fakeChannels{getErr: errors.New("offline")}
					}
					if mode == "topic lookup" {
						s.Topics = fakeTopics{err: errors.New("offline")}
					}
					if mode == "past deadline" || mode == "past cleanup" {
						s.Topics = typingTopics(func(ctx context.Context, m org.Membership, c, topic kernel.ID) (conversation.Topic, error) {
							delay := typing.SignalDeadline + time.Nanosecond
							if mode == "past cleanup" {
								state.Posted(kernel.ID{}, place, kernel.ID{}, token)
								delay = typing.ClosedRetention
							}
							time.Sleep(delay)
							state.Start(kernel.ID{}, place, typing.Typist{Handle: "alice"})
							time.Sleep(typing.Expiry - time.Second)
							before = state.Read(kernel.ID{}, place, kernel.ID{9})
							return fakeTopics{}.Get(ctx, m, c, topic)
						})
					}
					if mode == "limited" {
						s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) {
							l.signal = middleware.NewMemberRateLimiter(func() time.Time { return time.Unix(1, 0) })
						})
						for range 4 {
							s.MemberLimits.Signal().Allow(kernel.ID{}, kernel.ID{})
						}
						s.Channels = &fakeChannels{getErr: errors.New("must not look up")}
					}
				})
				path := view.ConversationURL("acme", place.Channel, &place.Topic) + "/typing"
				cookie, origin, body, status := "live", "", "composer="+token+"&member_id=foreign&display_name=foreign", 404
				switch mode {
				case "signed out":
					cookie = ""
				case "csrf":
					origin, status = "https://foreign.example", 403
				case "channel":
					path = strings.Replace(path, "01000000", "bad", 1)
				case "topic":
					path = strings.Replace(path, "02000000", "bad", 1)
				case "channel lookup", "topic lookup":
					status = 500
				case "missing token":
					body, status = "", 400
				case "past deadline", "past cleanup":
					status = 204
				case "malformed token":
					body, status = "composer=bad", 204
				case "duplicate token":
					body, status = body+"&composer=1", 400
				case "limited":
					status = 429
				}
				time.Sleep(typing.Expiry - time.Second)
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", origin)
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != status || !reflect.DeepEqual(before, state.Read(kernel.ID{}, place, kernel.ID{9})) {
					t.Fatalf("refusal %d changed state", w.Code)
				}
				if status == 404 && w.Body.String() != "404 page not found\n" {
					t.Fatal("scope disclosed")
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if got := state.Read(kernel.ID{}, place, kernel.ID{9}); len(got.Typists) != 0 || got.Generation != before.Generation+1 {
					t.Fatal("refusal extended deadline")
				}
			})
		})
	}
}

func TestTypingRouteSharedLimiter(t *testing.T) {
	state := typing.New(realtime.NewHub())
	h, _ := newLimitedHandler(t, asAlice, func(s *Services) {
		s.Typing = state
		s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) {
			l.signal = middleware.NewMemberRateLimiter(func() time.Time { return time.Unix(1, 0) })
		})
	})
	for i := range 5 {
		channel, topic := kernel.ID{byte(1 + i%2)}, kernel.ID{byte(10 + i)}
		defer state.Open(kernel.ID{}, kernel.ID{}, channel)()
		token := state.IssueComposer(kernel.ID{}, typing.Place{Channel: channel, Topic: topic}, kernel.ID{})
		w := serveForm(h, "POST", view.ConversationURL("acme", channel, &topic)+"/typing", "live", url.Values{"composer": {fmt.Sprint(token)}, "member_id": {fmt.Sprint(i)}, "display_name": {"foreign"}})
		want := 204
		if i == 4 {
			want = 429
		}
		if w.Code != want || w.Body.Len() != 0 {
			t.Fatalf("caller %d: %d %s", i, w.Code, w.Body.String())
		}
		got := state.Read(kernel.ID{}, typing.Place{Channel: channel, Topic: topic}, kernel.ID{9})
		if i < 4 && (len(got.Typists) != 1 || got.Typists[0].DisplayName != "Alice" || got.Typists[0].Handle != "alice") {
			t.Fatalf("untrusted identity: %+v", got)
		}
		if i == 4 && len(got.Typists) != 0 {
			t.Fatal("limited caller started typing")
		}
	}
}

type typingTopics func(context.Context, org.Membership, kernel.ID, kernel.ID) (conversation.Topic, error)

func (p typingTopics) Get(ctx context.Context, m org.Membership, channel, topic kernel.ID) (conversation.Topic, error) {
	return p(ctx, m, channel, topic)
}
