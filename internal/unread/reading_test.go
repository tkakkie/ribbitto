package unread

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type readingRunner func(context.Context, func(platform.Tx) error) error

func (f readingRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error { return f(ctx, fn) }

type readPublisher func(kernel.ID, kernel.ID)

func (f readPublisher) ReadChanged(org, member kernel.ID) { f(org, member) }

// Both writers exercise their real detection path; only their transaction-bound
// collaborators are faked, including a failure after topic candidate detection.
type readingState struct {
	changed bool
	err     error
}

func (s readingState) EventSeq(context.Context, kernel.ID) (int64, error) { return 10, nil }
func (s readingState) FirstMessageAfter(context.Context, kernel.ID, kernel.ID, int64) (int64, error) {
	return 0, nil
}
func (s readingState) HasMessages(context.Context, kernel.ID, kernel.ID, []int64, []int64) (bool, error) {
	return s.changed, nil
}
func (s readingState) Merge(context.Context, Scope, int64, Range) error { return s.err }
func (s readingState) MergeCovered(context.Context, Scope, int64, Range) ([]Range, error) {
	return []Range{{Lo: 2, Hi: 3}}, s.err
}
func (s readingState) ReadState() (int64, int64, []Range)                 { return 2, 1, nil }
func (s readingState) Add(context.Context, []int64, []int64, int64) error { return s.err }
func (s readingState) Ranges(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64, int64, []Range) ([]Range, error) {
	if s.changed {
		return []Range{{Lo: 2, Hi: 3}}, nil
	}
	return nil, nil
}

func TestReadingPublication(t *testing.T) {
	failure := errors.New("transaction failed")
	unknown := fmt.Errorf("%w: %w", platform.ErrCommitUnknown, failure)
	for _, kind := range []string{"feed", "topic"} {
		for _, tc := range []struct {
			name                                          string
			changed, cancel, disabled, outcome, committed bool
			writeErr, txErr                               error
		}{
			{name: "changed", changed: true},
			{name: "unchanged"},
			{name: "writer error", changed: true, writeErr: failure},
			{name: "rollback", changed: true, txErr: failure},
			{name: "commit failure", changed: true, txErr: failure},
			{name: "cancel after commit", changed: true, cancel: true},
			{name: "nil publisher", changed: true, disabled: true},
			// The server may have committed: a recount is right either way.
			{name: "commit outcome unknown, committed", changed: true, txErr: unknown, outcome: true, committed: true},
			{name: "commit outcome unknown, not committed", changed: true, txErr: unknown, outcome: true},
			{name: "unchanged, commit outcome unknown", txErr: unknown, outcome: true, committed: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				committed, completed, calls := false, false, 0
				state := readingState{changed: tc.changed, err: tc.writeErr}
				runner := readingRunner(func(ctx context.Context, fn func(platform.Tx) error) error {
					if err := fn(platform.Tx{}); err != nil {
						return err
					}
					if tc.txErr != nil {
						committed = tc.committed
						return tc.txErr
					}
					committed = true
					if tc.cancel {
						cancel()
					}
					return nil
				})
				scope := Scope{OrganizationID: kernel.ID{1}, MemberID: kernel.ID{2}, ChannelID: kernel.ID{3}}
				var publisher ReadPublisher = readPublisher(func(org, member kernel.ID) {
					calls++
					if !committed && !tc.outcome {
						t.Error("publication preceded successful commit")
					}
					if org != scope.OrganizationID || member != scope.MemberID {
						t.Error("publication scope differs from read scope")
					}
					if tc.cancel && ctx.Err() == nil {
						t.Error("request was not cancelled at the commit boundary")
					}
					completed = true
				})
				if tc.disabled {
					publisher = nil
				}
				cursor := func(platform.Tx) EventCursor { return state }
				feed := NewFeedWriter(func(platform.Tx) RangeWriter { return state }, func(platform.Tx) ChannelMessages { return state }, cursor)
				topic := NewTopicWriter(func(platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error) {
					return func(context.Context, TopicScope, int64) (PreparedTopic, error) { return state, nil }
				}, func(platform.Tx) TopicMessages { return state }, cursor)
				reading := NewReading(runner, feed, topic, publisher)
				var err error
				if kind == "feed" {
					err = reading.Feed(ctx, scope, 1, 2)
				} else {
					err = reading.Topic(ctx, TopicScope{Scope: scope, TopicID: kernel.ID{4}}, 1, 2)
				}
				wantError := tc.writeErr != nil || tc.txErr != nil
				if errors.Is(err, failure) != wantError {
					t.Fatalf("error=%v, want failure=%t", err, wantError)
				}
				want := tc.changed && !tc.disabled && (!wantError || tc.outcome)
				if (calls == 1) != want || calls > 1 || completed != want {
					t.Fatalf("publication calls=%d completed=%t before return, want published=%t", calls, completed, want)
				}
			})
		}
	}
}
