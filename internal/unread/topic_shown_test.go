package unread

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func (s readingState) ShownRanges(ctx context.Context, org, ch, topic kernel.ID, cursor, _, _, prefix, floor int64, read []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	return s.Ranges(ctx, org, ch, topic, cursor, prefix, floor, read)
}
func (s readingState) RunFloor(_ context.Context, _, _, _ kernel.ID, _, end, _, _ int64, _ []conversation.SequenceRange) (int64, error) {
	return end, nil
}

type topicShownSpy struct {
	*readSpy
	blocking, wantFloor int64
}

func (s topicShownSpy) ShownRanges(ctx context.Context, org, ch, topic kernel.ID, cursor, from, through, prefix, floor int64, read []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	if from != 5 || through != 8 {
		s.t.Fatal("wrong run bounds")
	}
	return s.Ranges(ctx, org, ch, topic, cursor, prefix, floor, read)
}
func (s topicShownSpy) RunFloor(ctx context.Context, org, ch, topic kernel.ID, from, end, prefix, floor int64, read []conversation.SequenceRange) (int64, error) {
	if ctx != s.t.Context() || org != s.scope.OrganizationID || ch != s.scope.ChannelID || topic != s.scope.TopicID || from != 5 || end != 9 || prefix != 2 || floor != 1 || !slices.Equal(read, s.readRanges) {
		s.t.Fatal("wrong guard arguments")
	}
	return s.blocking, s.step("guard")
}
func (s topicShownSpy) Add(ctx context.Context, los, his []int64, floor int64) error {
	if ctx != s.t.Context() || floor != s.wantFloor || len(los) != len(s.candidates) || (len(los) > 0 && (los[0] != 5 || his[0] != 9)) {
		s.t.Fatalf("Add(%v,%v,%d), want floor %d", los, his, floor, s.wantFloor)
	}
	s.addCalled = true
	return s.step("add")
}

func TestTopicReadShownFloor(t *testing.T) {
	failure := errors.New("rollback")
	for _, tc := range []struct {
		name, fail                                        string
		cursor, from, through, committed, blocking, floor int64
		invalid, empty, rollback                          bool
	}{
		{name: "no blocker", blocking: 9, floor: 8},
		{name: "partial raise", blocking: 4, floor: 3},
		{name: "moved blocker", blocking: 7, floor: 6},
		{name: "repeat", blocking: 9, floor: 8, empty: true},
		{name: "rollback", blocking: 9, floor: 8, rollback: true},
		{name: "cursor error", fail: "cursor"},
		{name: "prepare error", fail: "prepare"},
		{name: "candidate error", fail: "ranges"},
		{name: "guard error", fail: "guard"},
		{name: "write error", fail: "add", blocking: 9, floor: 8},
		{name: "F below one", cursor: 10, from: 0, through: 8, committed: 10, invalid: true},
		{name: "T below F", cursor: 10, from: 5, through: 4, committed: 10, invalid: true},
		{name: "T above S", cursor: 7, from: 5, through: 8, committed: 10, invalid: true},
		{name: "S above committed", cursor: 11, from: 5, through: 8, committed: 10, invalid: true},
		{name: "S MaxInt64 committed", cursor: math.MaxInt64, from: 5, through: 8, committed: math.MaxInt64, invalid: true},
		{name: "S negative", cursor: -1, from: 1, through: 1, committed: 10, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.invalid {
				tc.cursor, tc.from, tc.through, tc.committed = 10, 5, 8, 10
			}
			spy := topicShownSpy{readSpy: &readSpy{t: t, cursor: tc.cursor, committed: tc.committed, fail: tc.fail, failure: failure,
				scope:      TopicScope{Scope: Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}, TopicID: kernel.ID{4}},
				readRanges: []conversation.SequenceRange{{Lo: 3, Hi: 4}}, candidates: []conversation.SequenceRange{}}, blocking: tc.blocking, wantFloor: tc.floor}
			if !tc.empty {
				spy.candidates = []conversation.SequenceRange{{Lo: 5, Hi: 9}}
			}
			prepares, calls, completed := 0, 0, false
			writer := NewTopicWriter(func(platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error) {
				return func(ctx context.Context, scope TopicScope, joined int64) (PreparedTopic, error) {
					prepares++
					if ctx != t.Context() || scope != spy.scope || joined != 1 {
						t.Fatal("wrong preparation arguments")
					}
					return spy, spy.step("prepare")
				}
			}, func(platform.Tx) TopicMessages { return spy }, func(platform.Tx) EventCursor { return spy.readSpy })
			runner := readingRunner(func(ctx context.Context, fn func(platform.Tx) error) error {
				if err := fn(platform.Tx{}); err != nil {
					return err
				}
				completed = true
				if tc.rollback {
					return failure
				}
				return nil
			})
			reading := NewReading(runner, nil, writer, readPublisher(func(org, member kernel.ID) {
				calls++
				if !completed || org != spy.scope.OrganizationID || member != spy.scope.MemberID {
					t.Fatal("publication before commit or wrong scope")
				}
			}))
			err := reading.TopicShown(t.Context(), spy.scope, 1, tc.cursor, tc.from, tc.through)
			if tc.invalid {
				if !errors.Is(err, ErrInvalidCursor) || prepares != 0 || spy.addCalled || calls != 0 {
					t.Fatalf("refusal err=%v prepares=%d write=%t calls=%d", err, prepares, spy.addCalled, calls)
				}
			} else if tc.fail != "" || tc.rollback {
				if !errors.Is(err, failure) || calls != 0 {
					t.Fatalf("failure err=%v calls=%d", err, calls)
				}
			} else if err != nil || !spy.addCalled || calls != map[bool]int{true: 0, false: 1}[tc.empty] || !slices.Equal(spy.steps, []string{"cursor", "prepare", "state", "ranges", "guard", "add"}) {
				t.Fatalf("err=%v write=%t calls=%d steps=%v", err, spy.addCalled, calls, spy.steps)
			}
		})
	}
}
