package web

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestResolvePlace(t *testing.T) {
	offline := errors.New("offline")
	for _, tt := range []struct {
		name                 string
		channelErr, topicErr error
		rawTopic             string
		status, lookups      int
	}{
		{"feed", nil, nil, "", 200, 0},
		{"topic", nil, nil, streamTopic, 200, 1},
		{"missing channel", conversation.ErrChannelNotFound, nil, streamTopic, 404, 0},
		{"wrapped missing channel", fmt.Errorf("lookup: %w", conversation.ErrChannelNotFound), nil, "", 404, 0},
		{"missing topic", nil, conversation.ErrTopicNotFound, streamTopic, 404, 1},
		{"wrapped missing topic", nil, fmt.Errorf("lookup: %w", conversation.ErrTopicNotFound), streamTopic, 404, 1},
		{"channel failure", offline, nil, streamTopic, 500, 0},
		{"topic failure", nil, offline, streamTopic, 500, 1},
		{"unexpected channel error", org.ErrNotFound, nil, streamTopic, 500, 0},
		{"unexpected topic error", nil, org.ErrNotFound, streamTopic, 500, 1},
		{"malformed topic", nil, nil, "bad", 404, 0},
		{"channel error before malformed topic", offline, nil, "bad", 500, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var lookups [][3]kernel.ID
			p := channelPages{channels: &fakeChannels{getErr: tt.channelErr}, topics: fakeTopics{err: tt.topicErr, lookups: &lookups}}
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{9}}}
			r := httptest.NewRequest("GET", "/place", nil)
			w := httptest.NewRecorder()
			topic, ok := p.resolvePlace(w, r, m, kernel.ID{1}, tt.rawTopic)
			if w.Code != tt.status || ok != (tt.status == 200) {
				t.Fatalf("status=%d, ok=%t; want %d: %s", w.Code, ok, tt.status, w.Body.String())
			}
			if tt.status == 404 && w.Body.String() != "404 page not found\n" {
				t.Fatalf("scope disclosed: %q", w.Body.String())
			}
			if tt.status == 200 && (w.Body.Len() != 0 || (topic == nil) != (tt.rawTopic == "")) {
				t.Fatal("successful lookup wrote a response or lost the selection")
			}
			if topic != nil && *topic != (kernel.ID{6}) {
				t.Fatalf("selected topic: %v", *topic)
			}
			if len(lookups) != tt.lookups || (len(lookups) == 1 && lookups[0] != ([3]kernel.ID{{9}, {1}, {6}})) {
				t.Fatalf("topic lookup scope/order: %v", lookups)
			}
		})
	}
}
