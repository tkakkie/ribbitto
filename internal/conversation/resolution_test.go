package conversation_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type resolutionFake struct {
	postingFake
	state conversation.Topic
}

func (f *resolutionFake) GetTopic(_ context.Context, o, c, _ kernel.ID) (conversation.Topic, error) {
	f.scope(o, c)
	return f.state, f.step("topic")
}
func (f *resolutionFake) QuotedMessage(_ context.Context, o, c, _ kernel.ID) (conversation.Message, error) {
	f.scope(o, c)
	return conversation.Message{ID: kernel.ID{5}, TopicID: f.topic}, f.step("message")
}
func (f *resolutionFake) SetTopicConclusion(_ context.Context, o, c, topic, message kernel.ID) error {
	f.scope(o, c)
	if topic != (kernel.ID{4}) || message != (kernel.ID{}) && message != (kernel.ID{5}) {
		f.t.Fatal("wrong conclusion scope")
	}
	return f.step("write")
}
func (f *resolutionFake) Append(_ context.Context, o kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	f.scope(o, kernel.ID{3})
	c, topic, err := conversation.DecodeResolutionChanged(data)
	if err != nil || c != (kernel.ID{3}) || topic != (kernel.ID{4}) || seq != 7 || kind != conversation.KindResolutionChanged || audience != nil {
		f.t.Fatal("wrong resolution event")
	}
	return f.step("append")
}

func TestResolution(t *testing.T) {
	failure := errors.New("failed")
	for _, reopen := range []bool{false, true} {
		for _, name := range []string{"change", "noop", "replace", "default", "nonmember", "membership scope", "stale", "missing topic", "missing message", "sequence", "topic", "message", "write", "append", "commit"} {
			t.Run(name+map[bool]string{true: "/reopen", false: "/resolve"}[reopen], func(t *testing.T) {
				f := &resolutionFake{postingFake: postingFake{t: t, topic: kernel.ID{4}}, state: conversation.Topic{ID: kernel.ID{4}}}
				m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}, OrganizationID: kernel.ID{1}}}
				order := []string{"begin", "sequence", "topic", "message", "write", "append", "commit", "raise"}
				if reopen {
					f.state.ConclusionMessageID = kernel.ID{5}
					order = slices.Delete(order, 3, 4)
				}
				want, stop := error(nil), "raise"
				switch name {
				case "change":
				case "replace":
					f.state.ConclusionMessageID = kernel.ID{9}
				case "noop":
					f.state.ConclusionMessageID, stop = kernel.ID{5}, "message"
					if reopen {
						f.state.ConclusionMessageID, stop = kernel.ID{}, "topic"
					}
				case "default":
					f.state.IsDefault = true
					if !reopen {
						want, stop = conversation.ErrDefaultTopicResolution, "topic"
					} else {
						f.state.ConclusionMessageID, stop = kernel.ID{}, "topic"
					}
				case "nonmember":
					m.Member.ID, want, stop = kernel.ID{}, org.ErrNotFound, ""
				case "membership scope":
					m.Member.OrganizationID, want, stop = kernel.ID{9}, org.ErrNotFound, ""
				case "stale":
					f.topic = kernel.ID{9}
					if !reopen {
						want, stop = conversation.ErrConclusionConflict, "message"
					}
				case "missing topic":
					f.fail, f.err, want, stop = "topic", conversation.ErrTopicNotFound, conversation.ErrTopicNotFound, "topic"
				case "missing message":
					if !reopen {
						f.fail, f.err, want, stop = "message", conversation.ErrQuoteNotFound, conversation.ErrMessageNotFound, "message"
					}
				default:
					if name != "message" || !reopen {
						f.fail, f.err, want, stop = name, failure, failure, name
					}
				}
				order = order[:slices.Index(order, stop)+1]
				r := conversation.NewResolution(f, func(platform.Tx) conversation.Writer { return f }, func(platform.Tx) conversation.EventSequence { return f }, func(platform.Tx) conversation.EventAppender { return f }, f)
				var err error
				if reopen {
					err = r.Reopen(t.Context(), m, kernel.ID{3}, kernel.ID{4})
				} else {
					err = r.Resolve(t.Context(), m, kernel.ID{3}, kernel.ID{4}, kernel.ID{5})
				}
				if !errors.Is(err, want) || !slices.Equal(f.calls, order) || f.committed != slices.Contains(order, "raise") {
					t.Fatalf("error %v calls %v committed %v; want %v calls %v", err, f.calls, f.committed, want, order)
				}
			})
		}
	}
}

func TestResolutionChangedCodec(t *testing.T) {
	valid := conversation.EncodeResolutionChanged(kernel.ID{3}, kernel.ID{4})
	for _, data := range [][]byte{valid, nil, []byte(`{}`), []byte(`{"channel_id":1}`), []byte(`{"channel_id":"03000000-0000-0000-0000-000000000000"}`), []byte(`{"channel_id":"03000000-0000-0000-0000-000000000000","topic_id":"bad"}`)} {
		c, topics, err := conversation.RouteResolutionChanged(data)
		if good := slices.Equal(data, valid); (err == nil) != good || good && (c != (kernel.ID{3}) || topics != nil) {
			t.Fatalf("route %s: %v %v %v", data, c, topics, err)
		}
	}
}
