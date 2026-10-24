package unread

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func (s readingState) LastMessageBefore(context.Context, kernel.ID, kernel.ID, int64) (int64, error) {
	return 0, nil
}

type shownSpy struct {
	*readSpy
	from, previous int64
}

func (s shownSpy) LastMessageBefore(ctx context.Context, org, ch kernel.ID, from int64) (int64, error) {
	if ctx != s.t.Context() || org != s.scope.OrganizationID || ch != s.scope.ChannelID || from != s.from {
		s.t.Fatal("wrong predecessor scope or bound")
	}
	return s.previous, s.step("previous")
}

func TestFeedReadShown(t *testing.T) {
	failure := errors.New("boom")
	for _, tc := range []struct {
		name, fail, wrap string
		previous, next   int64
		empty            bool
	}{
		{name: "neighbours", previous: 3, next: 12},
		{name: "no neighbours"},
		{name: "empty coverage", empty: true},
		{name: "cursor error", fail: "cursor", wrap: "reading shown feed cursor limit"},
		{name: "previous error", fail: "previous", wrap: "finding previous shown feed message"},
		{name: "next error", fail: "next", wrap: "finding next shown feed message"},
		{name: "merge error", fail: "merge", wrap: "reading shown feed"},
		{name: "probe error", fail: "has", wrap: "checking newly read shown feed messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := shownSpy{readSpy: &readSpy{t: t, cursor: 8, joined: 2, committed: 20,
				firstAfter: tc.next, covered: []conversation.SequenceRange{{Lo: 5, Hi: 6}, {Lo: 7, Hi: 9}},
				emptyCovered: tc.empty, fail: tc.fail, failure: failure, readingState: readingState{changed: true}}, from: 5, previous: tc.previous}
			writer := NewFeedWriter(func(platform.Tx) RangeWriter { return spy.readSpy }, func(platform.Tx) ChannelMessages { return spy }, func(platform.Tx) EventCursor { return spy.readSpy })
			changed, err := writer.ReadShown(t.Context(), platform.Tx{}, spy.scope.Scope, 2, 15, 5, 8)
			if tc.fail != "" {
				if changed || !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), tc.wrap+": ") {
					t.Fatalf("changed=%t err=%v, want wrapped %s", changed, err, tc.wrap)
				}
				return
			}
			want := conversation.SequenceRange{Hi: 9}
			if tc.previous != 0 {
				want.Lo = tc.previous + 1
			}
			if tc.next != 0 {
				want.Hi = tc.next
			}
			steps := []string{"cursor", "previous", "next", "merge"}
			if !tc.empty {
				steps = append(steps, "has")
			}
			if err != nil || changed == tc.empty || spy.merged != want || !slices.Equal(spy.steps, steps) {
				t.Fatalf("changed=%t err=%v range=%v steps=%v; want %v %v", changed, err, spy.merged, spy.steps, want, steps)
			}
		})
	}
}

func TestReadShownInvalid(t *testing.T) {
	for _, bounds := range [][3]int64{{10, 6, 5}, {10, 1, 11}, {10, 0, 5}, {11, 1, 5}, {math.MaxInt64, 1, 5}, {-1, 1, 1}} {
		spy := &readSpy{t: t, committed: 10}
		if bounds[0] == math.MaxInt64 {
			spy.committed = math.MaxInt64
		}
		writer := NewFeedWriter(func(platform.Tx) RangeWriter { t.Fatal("invalid read reached writer"); return nil }, func(platform.Tx) ChannelMessages { t.Fatal("invalid read reached messages"); return nil }, func(platform.Tx) EventCursor { return spy })
		changed, err := writer.ReadShown(t.Context(), platform.Tx{}, Scope{}, 0, bounds[0], bounds[1], bounds[2])
		if changed || !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("bounds=%v changed=%t err=%v", bounds, changed, err)
		}
	}
}

func TestReadingPublishesShown(t *testing.T) {
	failure := errors.New("rollback")
	for _, tc := range []struct {
		name    string
		changed bool
		err     error
	}{{"changed", true, nil}, {"noop", false, nil}, {"rollback", true, failure}} {
		t.Run(tc.name, func(t *testing.T) {
			completed, calls := false, 0
			state := readingState{changed: tc.changed}
			feed := NewFeedWriter(func(platform.Tx) RangeWriter { return state }, func(platform.Tx) ChannelMessages { return state }, func(platform.Tx) EventCursor { return state })
			runner := readingRunner(func(ctx context.Context, fn func(platform.Tx) error) error {
				if err := fn(platform.Tx{}); err != nil {
					return err
				}
				completed = true
				return tc.err
			})
			scope := Scope{OrganizationID: kernel.ID{1}, MemberID: kernel.ID{2}, ChannelID: kernel.ID{3}}
			reading := NewReading(runner, feed, nil, readPublisher(func(org, member kernel.ID) {
				calls++
				if !completed || org != scope.OrganizationID || member != scope.MemberID {
					t.Fatal("publication before completion or outside scope")
				}
			}))
			err := reading.FeedShown(t.Context(), scope, 1, 10, 2, 3)
			want := 0
			if tc.changed && tc.err == nil {
				want = 1
			}
			if !errors.Is(err, tc.err) || calls != want {
				t.Fatalf("err=%v calls=%d, want %v %d", err, calls, tc.err, want)
			}
		})
	}
}
