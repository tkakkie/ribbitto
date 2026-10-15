package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func TestTypingDeliveryScopes(t *testing.T) {
	for _, mode := range []string{"topic A", "feed", "topic B", "channel", "organisation", "interest", "denied", "self"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub := realtime.NewHub()
				state := typing.New(hub)
				place, person := typing.Place{Channel: kernel.ID{1}, Topic: kernel.ID{6}}, typing.Typist{Member: kernel.ID{2}, DisplayName: "Alice", Handle: "alice"}
				defer state.Open(kernel.ID{}, person.Member, place.Channel)()
				token := state.IssueComposer(kernel.ID{}, place, person.Member)
				if !state.Signal(kernel.ID{}, place, person, token, time.Now()) {
					t.Fatal("signal refused")
				}
				member := org.Membership{Organization: org.Organization{Slug: "acme"}, Member: org.Member{ID: kernel.ID{3}}}
				query := "want=typing&channel=" + streamChannel + "&topic=" + streamTopic
				switch mode {
				case "feed":
					query = "want=typing&channel=" + streamChannel
				case "topic B":
					query = strings.Replace(query, streamTopic, "07000000-0000-0000-0000-000000000000", 1)
				case "channel":
					query = strings.Replace(query, streamChannel, "02000000-0000-0000-0000-000000000000", 1)
				case "organisation":
					member.Organization.ID = kernel.ID{9}
				case "interest":
					query = strings.Replace(query, "want=typing", "want=messages", 1)
				case "self":
					member.Member.ID = person.Member
				}
				seen := 0
				p := channelPages{channels: &fakeChannels{}, topics: fakeTopics{}, stream: &Streaming{Hub: hub, Events: quietLog{}, Owners: map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestTyping: NewTypingOwner(state)}, Authorizer: streamAllow(mode != "denied"), Sessions: laterSession{hub: hub, session: identity.Session{ID: kernel.ID{0x51}, ExpiresAt: time.Now().Add(time.Hour)}, seen: &seen}}}
				h := middleware.Session(oneSession{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.events(w, r, member) }))
				r := httptest.NewRequest("GET", "/events?after=17&"+query, nil)
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				ctx, cancel := context.WithCancel(r.Context())
				timer := time.AfterFunc(time.Second, cancel)
				defer timer.Stop()
				defer cancel()
				w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
				h.ServeHTTP(w, r.WithContext(ctx))
				body := w.Body.String()
				frame := mode != "interest" && mode != "denied"
				named := mode == "topic A" || mode == "feed"
				if w.Code != 200 || strings.Contains(body, "event: typing") != frame || strings.Contains(body, "Alice") != named || strings.Contains(body, "id:") {
					t.Fatalf("scoped delivery: %d %q", w.Code, body)
				}
			})
		})
	}
}

func TestTypingOwnerRendering(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			catalogues, err := i18n.New(slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			state := typing.New(realtime.NewHub())
			place := typing.Place{Channel: kernel.ID{1}, Topic: kernel.ID{6}}
			sub := realtime.Subscription{Channel: place.Channel, Topic: &place.Topic, Member: kernel.ID{1}}
			owner := NewTypingOwner(state)
			for count := 0; count <= 6; count++ {
				if count > 0 {
					id := kernel.ID{byte(count)}
					defer state.Open(sub.Organization, id, place.Channel)()
					state.Start(sub.Organization, place, typing.Typist{Member: id, DisplayName: fmt.Sprintf("<Name%d>", count), Handle: fmt.Sprintf("handle%d", count)})
				}
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set("Accept-Language", lang)
				catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					frame, err := owner.Read(r.Context(), sub, -1)
					if err != nil {
						t.Fatal(err)
					}
					body := string(frame.Outgoing.Data)
					suffix := " are typing…"
					more := " and 2 others"
					if lang == "ja" {
						suffix = "が入力中…"
						more = "、ほか2人"
					}
					if count == 2 && lang == "en" {
						suffix = " is typing…"
					}
					// Two names join with "and"; one remaining typist is singular.
					if lang == "en" && ((count == 3 && !strings.Contains(body, "</span> and ")) || (count == 5 && (!strings.Contains(body, " and 1 other ") || strings.Contains(body, "others")))) {
						t.Fatalf("count %d English wording: %s", count, body)
					}
					if frame.Outgoing.Name != "typing" || frame.Generation != int64(count) || (body == "") != (count <= 1) || strings.Contains(body, "<Name") || strings.Contains(body, "handle1") || strings.Count(body, "<bdi") != min(3, max(0, count-1)) || (count > 1 && !strings.Contains(body, suffix)) || (count == 6 && !strings.Contains(body, more)) {
						t.Fatalf("count %d rendering: %+v %s", count, frame, body)
					}
				})).ServeHTTP(httptest.NewRecorder(), r)
			}
		})
	}
}

type typingReadHook struct {
	owner realtime.EphemeralOwner
	hook  func(int64, realtime.EphemeralFrame)
}

func (o typingReadHook) Read(ctx context.Context, sub realtime.Subscription, after int64) (realtime.EphemeralFrame, error) {
	frame, err := o.owner.Read(ctx, sub, after)
	o.hook(after, frame)
	return frame, err
}

type typingDeliveryLog struct{ reads int }

func (l *typingDeliveryLog) EventsAfter(context.Context, kernel.ID, int64, int) ([]realtime.Event, error) {
	l.reads++
	if l.reads > 10 {
		return nil, io.EOF
	}
	return nil, nil
}

type typingDeliverySender struct{ frames []realtime.Outgoing }

func (s *typingDeliverySender) Send(_ context.Context, out realtime.Outgoing) error {
	s.frames = append(s.frames, out)
	return nil
}
func (*typingDeliverySender) Heartbeat(context.Context) error { return nil }

func TestTypingDeliveryGenerations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := realtime.NewHub()
		state := typing.New(hub)
		place, person := typing.Place{Channel: kernel.ID{1}, Topic: kernel.ID{6}}, typing.Typist{Member: kernel.ID{2}, Handle: "alice"}
		closeStream := state.Open(kernel.ID{}, person.Member, place.Channel)
		defer closeStream()
		owner := NewTypingOwner(state)
		log := &typingDeliveryLog{}
		send := &typingDeliverySender{}
		sub := realtime.Subscription{Channel: place.Channel, Topic: &place.Topic, Member: kernel.ID{3}, Interests: []realtime.Interest{realtime.InterestTyping}}
		reads, checks := 0, 0
		stream := realtime.Stream{Hub: hub, Events: log, Owners: map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestTyping: typingReadHook{owner, func(after int64, frame realtime.EphemeralFrame) {
			reads++
			if reads == 1 {
				if after != -1 || frame.Generation != 0 || len(frame.Outgoing.Data) != 0 {
					t.Error("missing empty connect snapshot")
				}
				state.Start(sub.Organization, place, person)
			}
		}}}, Authorizer: typingDeliveryAuthorizer{func(event realtime.Event) bool {
			checks++
			if reads < checks || event.ChannelID != place.Channel {
				t.Error("authorization did not follow render")
			}
			return true
		}}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan int64, 1)
		go func() { cursor, _ := stream.Run(ctx, sub, 17, send); done <- cursor }()
		synctest.Wait()
		if reads != 2 || len(send.frames) != 2 || !strings.Contains(string(send.frames[1].Data), "@alice") {
			t.Fatal("raise during read was acknowledged without delivery")
		}
		other := place
		other.Topic = kernel.ID{7}
		state.Start(sub.Organization, other, person)
		synctest.Wait()
		if reads != 3 || log.reads != 1 || checks != 2 || len(send.frames) != 2 {
			t.Fatalf("unrelated wake repeated frame or spun: reads=%d log=%d checks=%d", reads, log.reads, checks)
		}
		closeStream()
		synctest.Wait()
		if len(send.frames) != 3 || len(send.frames[2].Data) != 0 {
			t.Fatal("last typist did not clear")
		}
		cancel()
		if <-done != 17 {
			t.Fatal("typing moved durable cursor")
		}
		for _, frame := range send.frames {
			if !frame.Ephemeral || frame.ID != 0 {
				t.Fatal("typing carried SSE ID")
			}
		}
		restored, err := owner.Read(t.Context(), sub, -1)
		if err != nil || restored.Outgoing.Name != "typing" || len(restored.Outgoing.Data) != 0 {
			t.Fatal("empty reconnect did not replace stale content")
		}
	})
}

type typingDeliveryAuthorizer struct{ allow func(realtime.Event) bool }

func (a typingDeliveryAuthorizer) MayReceive(_ context.Context, _ kernel.ID, _ string, event realtime.Event) (bool, error) {
	return a.allow(event), nil
}
