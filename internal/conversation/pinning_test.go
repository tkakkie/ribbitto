package conversation_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type pinFake struct {
	postingFake
	pinned bool
	count  int64
}

func (f *pinFake) GetDefaultTopic(ctx context.Context, o, c kernel.ID) (conversation.Topic, error) {
	if owner := map[kernel.ID]kernel.ID{{3}: {1}, {9}: {9}}[c]; owner != o {
		return conversation.Topic{}, f.step("default")
	}
	return f.postingFake.GetDefaultTopic(ctx, o, c)
}
func (f *pinFake) GetMessagePin(_ context.Context, o, c, id kernel.ID) (conversation.PinState, error) {
	f.scope(o, c)
	if channel, exists := map[kernel.ID]kernel.ID{{5}: {3}, {9}: {4}}[id]; !exists || channel != c {
		return conversation.PinState{}, conversation.ErrMessageNotFound
	}
	var state conversation.PinState
	if f.pinned {
		state.PinnedAt = time.Unix(1, 0)
	}
	return state, f.step("inspect")
}
func (f *pinFake) CountChannelPins(_ context.Context, o, c kernel.ID) (int64, error) {
	f.scope(o, c)
	return f.count, f.step("count")
}
func (f *pinFake) SetMessagePin(_ context.Context, o, c, id, member kernel.ID) error {
	f.scope(o, c)
	if id != (kernel.ID{5}) || member != (kernel.ID{2}) {
		f.t.Fatal("wrong pin target/pinner")
	}
	return f.step("write")
}
func (f *pinFake) ClearMessagePin(ctx context.Context, o, c, id kernel.ID) error {
	return f.SetMessagePin(ctx, o, c, id, kernel.ID{2})
}
func (f *pinFake) Append(_ context.Context, o kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, data []byte) error {
	f.scope(o, kernel.ID{3})
	c, id, err := conversation.DecodePinsChanged(data)
	if err != nil || c != (kernel.ID{3}) || id != (kernel.ID{5}) || seq != 7 || kind != conversation.KindPinsChanged || audience != nil {
		f.t.Fatal("wrong invalidation")
	}
	return f.step("append")
}

func TestPinning(t *testing.T) {
	failure := errors.New("failed")
	for _, pin := range []bool{true, false} {
		for _, name := range []string{"change", "noop", "limit", "organisation", "member", "membership scope", "channel", "other channel message", "missing message", "begin", "sequence", "default", "inspect", "count", "write", "append", "commit"} {
			t.Run(name+map[bool]string{true: "/pin", false: "/unpin"}[pin], func(t *testing.T) {
				if name == "count" && !pin {
					return
				}
				f := &pinFake{postingFake: postingFake{t: t}, pinned: !pin}
				m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}, OrganizationID: kernel.ID{1}}}
				channel, id, want, stop := kernel.ID{3}, kernel.ID{5}, error(nil), "raise"
				order := []string{"begin", "sequence", "default", "inspect", "write", "append", "commit", "raise"}
				if pin {
					order = slices.Insert(order, 4, "count")
				}
				switch name {
				case "noop":
					f.pinned, stop = pin, "inspect"
				case "limit":
					f.count = conversation.MaxChannelPins
					if pin {
						want, stop = conversation.ErrPinLimit, "count"
					}
				case "organisation":
					m.Organization.ID, m.Member.OrganizationID = kernel.ID{}, kernel.ID{}
					want, stop = org.ErrNotFound, ""
				case "member":
					m.Member.ID = kernel.ID{}
					want, stop = org.ErrNotFound, ""
				case "membership scope":
					m.Member.OrganizationID = kernel.ID{9}
					want, stop = org.ErrNotFound, ""
				case "channel":
					channel, f.fail, f.err = kernel.ID{9}, "default", conversation.ErrTopicNotFound
					want, stop = conversation.ErrChannelNotFound, "default"
				case "other channel message", "missing message":
					id = map[string]kernel.ID{"other channel message": {9}, "missing message": {}}[name]
					want, stop = conversation.ErrMessageNotFound, "default"
				case "change":
				default:
					f.fail, f.err, want, stop = name, failure, failure, name
				}
				order = order[:slices.Index(order, stop)+1]
				p := conversation.NewPinning(f, func(platform.Tx) conversation.Writer { return f }, func(platform.Tx) conversation.PinWriter { return f }, func(platform.Tx) conversation.EventSequence { return f }, func(platform.Tx) conversation.EventAppender { return f }, f)
				op := map[bool]func(context.Context, org.Membership, kernel.ID, kernel.ID) error{true: p.Pin, false: p.Unpin}[pin]
				err := op(t.Context(), m, channel, id)
				if !errors.Is(err, want) || !slices.Equal(f.calls, order) || f.committed != slices.Contains(order, "raise") {
					t.Fatalf("%v, calls %v committed %v; want %v, %v", err, f.calls, f.committed, want, order)
				}
			})
		}
	}
}

func TestPinsChangedCodec(t *testing.T) {
	valid := conversation.EncodePinsChanged(kernel.ID{3}, kernel.ID{5})
	for _, data := range [][]byte{valid, nil, []byte(`{}`), []byte(`{"channel_id":null}`), []byte(`{"channel_id":1}`), []byte(`{"channel_id":"03000000-0000-0000-0000-000000000000"}`), []byte(`{"channel_id":"03000000-0000-0000-0000-000000000000","message_id":"bad"}`)} {
		c, topics, err := conversation.RoutePinsChanged(data)
		wantValid := slices.Equal(data, valid)
		if (err == nil) != wantValid || wantValid && (c != (kernel.ID{3}) || topics != nil) {
			t.Fatalf("route %s: %v %v %v", data, c, topics, err)
		}
	}
}
