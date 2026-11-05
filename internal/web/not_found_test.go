package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

type notFoundMessages struct {
	fakeMessages
	stage   string
	failure error
}

func (f notFoundMessages) Locate(context.Context, org.Membership, kernel.ID) (conversation.Location, error) {
	if f.stage == "locate" {
		return conversation.Location{ChannelID: kernel.ID{99}}, f.failure
	}
	return conversation.Location{ChannelID: kernel.ID{1}}, nil
}
func (f notFoundMessages) Many(context.Context, org.Membership, kernel.ID, []kernel.ID) ([]conversation.Entry, error) {
	return []conversation.Entry{{Message: conversation.Message{Body: "foreign message"}}}, f.failure
}
func (f notFoundMessages) Members(context.Context, org.Membership, kernel.ID, *kernel.ID) (conversation.MembersPage, error) {
	return conversation.MembersPage{}, f.failure
}
func (f notFoundMessages) ReadPins(context.Context, org.Membership, kernel.ID) (conversation.PinsSnapshot, error) {
	return conversation.PinsSnapshot{}, f.failure
}

type notFoundChannels struct {
	Channels
	failure error
}

func (f notFoundChannels) SetImage(context.Context, org.Membership, kernel.ID, string) error {
	return f.failure
}

type notFoundReading struct {
	Reading
	failure error
}

func (f notFoundReading) Feed(context.Context, unread.Scope, int64, int64) error { return f.failure }
func (f notFoundReading) Topic(context.Context, unread.TopicScope, int64, int64) error {
	return f.failure
}

func TestNotFoundRoutes(t *testing.T) {
	const channel = "/organizations/acme/channels/01000000-0000-0000-0000-000000000000"
	const topic = channel + "/topics/06000000-0000-0000-0000-000000000000"
	const message = "/organizations/acme/messages/08000000-0000-0000-0000-000000000000"
	reader := func(stage string) func(*Services, error) {
		return func(s *Services, err error) {
			f := notFoundMessages{fakeMessages: populatedMessages(), stage: stage, failure: err}
			if stage == "page" {
				f.err = err
			}
			s.Messages = f
		}
	}
	channelError := func(s *Services, err error) { s.Channels = &fakeChannels{getErr: err} }
	topicError := func(s *Services, err error) { s.Topics = fakeTopics{err: err} }
	postError := func(s *Services, err error) { s.Posting = &fakePoster{err: err} }
	routes := []struct {
		name, method, path, form string
		inject                   func(*Services, error)
		pins, quote              bool
	}{
		{name: "list sidebar", method: "GET", path: "/organizations/acme/channels?channel=01000000-0000-0000-0000-000000000000", inject: reader("page")},
		{name: "feed page", method: "GET", path: channel, inject: reader("page")},
		{name: "topic page", method: "GET", path: topic, inject: reader("page")},
		{name: "older page", method: "GET", path: channel + "?before=9", inject: reader("page")},
		{name: "around page", method: "GET", path: channel + "?around=9", inject: reader("page")},
		{name: "overview", method: "GET", path: channel + "/overview", inject: reader("page")},
		{name: "feed members follow-up", method: "GET", path: channel + "/members", inject: reader("members")},
		{name: "topic overview", method: "GET", path: topic + "/overview", inject: reader("page")},
		{name: "members follow-up", method: "GET", path: topic + "/members", inject: reader("members")},
		{name: "post channel", method: "POST", path: channel, form: "body=hello", inject: channelError},
		{name: "post topic send", method: "POST", path: topic, form: "body=hello", inject: postError, quote: true},
		{name: "post send", method: "POST", path: channel, form: "body=hello", inject: postError, quote: true},
		{name: "post topic validation", method: "POST", path: topic, form: "body=", quote: true, inject: func(s *Services, err error) {
			s.Posting = &fakePoster{err: conversation.ErrInvalidBody}
			topicError(s, err)
		}},
		{name: "topic image write", method: "POST", path: topic + "/overview", form: "image=leaf", inject: func(s *Services, err error) { s.Channels = notFoundChannels{Channels: s.Channels, failure: err} }},
		{name: "image write", method: "POST", path: channel + "/overview", form: "image=leaf", inject: func(s *Services, err error) { s.Channels = notFoundChannels{Channels: s.Channels, failure: err} }},
		{name: "branch", method: "POST", path: channel + "/branch", form: "message=08000000-0000-0000-0000-000000000000&from=06000000-0000-0000-0000-000000000000&name=design", inject: func(s *Services, err error) { s.Branching = &fakeBrancher{err: err} }},
		{name: "branch feedback scope", method: "POST", path: channel + "/branch", form: "from=bad", inject: channelError},
		{name: "pin", method: "POST", path: channel + "/messages/08000000-0000-0000-0000-000000000000/pin", inject: func(s *Services, err error) { s.Pinning = &fakePinning{err: err} }},
		{name: "unpin", method: "POST", path: channel + "/messages/08000000-0000-0000-0000-000000000000/unpin", inject: func(s *Services, err error) { s.Pinning = &fakePinning{err: err} }},
		{name: "pin follow-up", method: "POST", path: channel + "/messages/08000000-0000-0000-0000-000000000000/pin", inject: reader("pins"), pins: true},
		{name: "unpin follow-up", method: "POST", path: channel + "/messages/08000000-0000-0000-0000-000000000000/unpin", inject: reader("pins"), pins: true},
		{name: "quote read", method: "POST", path: channel + "/quote", form: "quote=08000000-0000-0000-0000-000000000000", inject: reader("many")},
		{name: "message link", method: "GET", path: message, inject: reader("locate")},
		{name: "actions locate", method: "GET", path: message + "/actions", inject: reader("locate")},
		{name: "actions follow-up", method: "GET", path: message + "/actions", inject: reader("many")},
		{name: "resolve channel", method: "GET", path: topic + "?before=bad", inject: channelError},
		{name: "resolve topic", method: "GET", path: topic + "?before=bad", inject: topicError},
		{name: "read feed", method: "POST", path: channel + "/read", form: "cursor=9", inject: func(s *Services, err error) { s.Reading = notFoundReading{failure: err} }},
		{name: "read topic", method: "POST", path: topic + "/read", form: "cursor=9", inject: func(s *Services, err error) { s.Reading = notFoundReading{failure: err} }},
	}
	failures := []struct {
		name   string
		err    error
		status int
	}{
		{"channel", conversation.ErrChannelNotFound, 404},
		{"topic", conversation.ErrTopicNotFound, 404},
		{"message", conversation.ErrMessageNotFound, 404},
		{"membership", org.ErrNotFound, 404},
		{"unknown", errors.New("foreign organization details"), 500},
		{"quote", conversation.ErrQuoteNotFound, 500},
	}
	for _, route := range routes {
		for _, failure := range failures {
			for _, wrapped := range []bool{false, true} {
				for _, hx := range []bool{false, true} {
					// Topic validation is an enhanced-post path; pin snapshots require htmx.
					if !hx && (route.pins || route.name == "post topic validation") {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/wrapped=%t/hx=%t", route.name, failure.name, wrapped, hx), func(t *testing.T) {
						err := failure.err
						if wrapped {
							err = fmt.Errorf("foreign organization details: %w", err)
						}
						handler, _ := newLimitedHandler(t, asAlice, func(s *Services) {
							s.Pinning = &fakePinning{}
							s.Branching = &fakeBrancher{}
							route.inject(s, err)
						})
						r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.form))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
						if hx {
							r.Header.Set("HX-Request", "true")
						}
						if route.pins {
							r.Header.Set("X-Pins-Section", "true")
						}
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, r)
						want := failure.status
						if route.quote && failure.name == "quote" {
							want = 404
						}
						if w.Code != want {
							t.Fatalf("status %d, want %d", w.Code, want)
						}
						if want == 404 && (w.Body.String() != "404 page not found\n" || w.Header().Get("Location") != "" || w.Header().Get("HX-Redirect") != "" || w.Header().Get("HX-Location") != "") {
							t.Fatalf("404 disclosed content or a destination: headers %v, body %q", w.Header(), w.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{conversation.ErrChannelNotFound, true},
		{conversation.ErrTopicNotFound, true},
		{conversation.ErrMessageNotFound, true},
		{org.ErrNotFound, true},
		{conversation.ErrQuoteNotFound, false},
		{conversation.ErrInvalidBody, false},
		{errors.New("channel not found"), false},
		{nil, false},
	} {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			if wrapped && err != nil {
				err = fmt.Errorf("lookup: %w", err)
			}
			if got := isNotFound(err); got != tc.want {
				t.Errorf("isNotFound(%v) = %t, want %t", err, got, tc.want)
			}
		}
	}
}
