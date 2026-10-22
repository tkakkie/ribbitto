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

// A zero field uses the readingState default.
type readSpy struct {
	readingState
	t            *testing.T
	scope        TopicScope
	joined       int64
	cursor       int64
	committed    int64
	firstAfter   int64
	covered      []conversation.SequenceRange
	emptyCovered bool
	readRanges   []conversation.SequenceRange
	candidates   []conversation.SequenceRange
	fail         string
	failure      error
	steps        []string
	merged       conversation.SequenceRange
	addedLos     []int64
	addedHis     []int64
	addedCursor  int64
	addCalled    bool
}

func (s *readSpy) step(name string) error {
	s.steps = append(s.steps, name)
	if s.fail == name {
		return s.failure
	}
	return nil
}

func (s *readSpy) EventSeq(ctx context.Context, org kernel.ID) (int64, error) {
	if ctx != s.t.Context() || org != s.scope.OrganizationID {
		s.t.Fatalf("EventSeq args = (%v, %v), want (%v, %v)", ctx, org, s.t.Context(), s.scope.OrganizationID)
	}
	if err := s.step("cursor"); err != nil {
		return 0, err
	}
	if s.committed != 0 {
		return s.committed, nil
	}
	return s.readingState.EventSeq(ctx, org)
}

func (s *readSpy) FirstMessageAfter(ctx context.Context, org, ch kernel.ID, cursor int64) (int64, error) {
	if ctx != s.t.Context() || org != s.scope.OrganizationID || ch != s.scope.ChannelID || cursor != s.cursor {
		s.t.Fatalf("FirstMessageAfter args = (%v, %v, %v, %d)", ctx, org, ch, cursor)
	}
	if err := s.step("next"); err != nil {
		return 0, err
	}
	if s.firstAfter != 0 {
		return s.firstAfter, nil
	}
	return s.readingState.FirstMessageAfter(ctx, org, ch, cursor)
}

func (s *readSpy) MergeCovered(ctx context.Context, scope Scope, joined int64, r conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	if ctx != s.t.Context() || scope != s.scope.Scope || joined != s.joined {
		s.t.Fatalf("MergeCovered args = (%v, %+v, %d, %+v)", ctx, scope, joined, r)
	}
	s.merged = r
	if err := s.step("merge"); err != nil {
		return nil, err
	}
	if s.emptyCovered {
		return nil, nil
	}
	if s.covered != nil {
		return s.covered, nil
	}
	return s.readingState.MergeCovered(ctx, scope, joined, r)
}

func (s *readSpy) HasMessages(ctx context.Context, org, ch kernel.ID, los, his []int64) (bool, error) {
	wantCovered := s.covered
	if wantCovered == nil {
		wantCovered, _ = s.readingState.MergeCovered(ctx, s.scope.Scope, s.joined, s.merged)
	}
	wantLos, wantHis := make([]int64, len(wantCovered)), make([]int64, len(wantCovered))
	for i, r := range wantCovered {
		wantLos[i], wantHis[i] = r.Lo, r.Hi
	}
	if ctx != s.t.Context() || org != s.scope.OrganizationID || ch != s.scope.ChannelID || !slices.Equal(los, wantLos) || !slices.Equal(his, wantHis) {
		s.t.Fatalf("HasMessages args = (%v, %v, %v, %v), want (%v, %v, %v, %v)", org, ch, los, his, s.scope.OrganizationID, s.scope.ChannelID, wantLos, wantHis)
	}
	if err := s.step("has"); err != nil {
		return false, err
	}
	return s.readingState.HasMessages(ctx, org, ch, los, his)
}

func (s *readSpy) ReadState() (int64, int64, []conversation.SequenceRange) {
	_ = s.step("state")
	prefix, floor, _ := s.readingState.ReadState()
	return prefix, floor, s.readRanges
}

func (s *readSpy) Ranges(ctx context.Context, org, ch, topic kernel.ID, cursor, prefix, floor int64, read []conversation.SequenceRange) ([]conversation.SequenceRange, error) {
	wantPrefix, wantFloor, _ := s.readingState.ReadState()
	if ctx != s.t.Context() || org != s.scope.OrganizationID || ch != s.scope.ChannelID || topic != s.scope.TopicID || cursor != s.cursor || prefix != wantPrefix || floor != wantFloor || !slices.Equal(read, s.readRanges) {
		s.t.Fatalf("Ranges args = (%v, %v, %v, %d, %d, %d, %v)", org, ch, topic, cursor, prefix, floor, read)
	}
	if err := s.step("ranges"); err != nil {
		return nil, err
	}
	if s.candidates != nil {
		return s.candidates, nil
	}
	return s.readingState.Ranges(ctx, org, ch, topic, cursor, prefix, floor, read)
}

func (s *readSpy) Add(ctx context.Context, los, his []int64, cursor int64) error {
	if ctx != s.t.Context() || cursor != s.cursor {
		s.t.Fatalf("Add args = (%v, %v, %v, %d)", ctx, los, his, cursor)
	}
	s.addCalled, s.addedLos, s.addedHis, s.addedCursor = true, slices.Clone(los), slices.Clone(his), cursor
	if err := s.step("add"); err != nil {
		return err
	}
	return s.readingState.Add(ctx, los, his, cursor)
}

func TestFeedWriterRead(t *testing.T) {
	failure := errors.New("boom")
	scope := Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}
	joined := int64(5)
	for _, tc := range []struct {
		name         string
		cursor       int64
		committed    int64
		firstAfter   int64
		covered      []conversation.SequenceRange
		emptyCovered bool
		changed      bool
		fail         string
		wantMerge    conversation.SequenceRange
		wantChanged  bool
		wantErrIs    error
		wantErr      string
		wantSteps    []string
	}{
		{name: "negative cursor", cursor: -1, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor"},
		// Committed is MaxInt64 if reached so cursor > committed cannot mask removing the MaxInt64 guard.
		{name: "max int64 cursor", cursor: math.MaxInt64, committed: math.MaxInt64, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor"},
		{name: "cursor above committed", cursor: 11, committed: 10, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor", wantSteps: []string{"cursor"}},
		{name: "next is cursor plus one when no later message", cursor: 0, changed: true, wantMerge: conversation.SequenceRange{Lo: 0, Hi: 1}, wantChanged: true, wantSteps: []string{"cursor", "next", "merge", "has"}},
		{name: "single covered range checks messages", cursor: 4, covered: []conversation.SequenceRange{{Lo: 2, Hi: 5}}, changed: true, wantMerge: conversation.SequenceRange{Lo: 0, Hi: 5}, wantChanged: true, wantSteps: []string{"cursor", "next", "merge", "has"}},
		{name: "next taken from first message after cursor", cursor: 10, firstAfter: 15, covered: []conversation.SequenceRange{{Lo: 2, Hi: 5}, {Lo: 7, Hi: 15}}, wantMerge: conversation.SequenceRange{Lo: 0, Hi: 15}, wantSteps: []string{"cursor", "next", "merge", "has"}},
		{name: "empty covered skips has messages", cursor: 4, emptyCovered: true, changed: true, wantMerge: conversation.SequenceRange{Lo: 0, Hi: 5}, wantSteps: []string{"cursor", "next", "merge"}},
		{name: "cursor limit error", cursor: 4, fail: "cursor", wantErrIs: failure, wantErr: "reading feed cursor limit: boom", wantSteps: []string{"cursor"}},
		{name: "next message error", cursor: 4, fail: "next", wantErrIs: failure, wantErr: "finding next feed message: boom", wantSteps: []string{"cursor", "next"}},
		{name: "merge covered error", cursor: 4, fail: "merge", wantMerge: conversation.SequenceRange{Lo: 0, Hi: 5}, wantErrIs: failure, wantErr: "reading feed: boom", wantSteps: []string{"cursor", "next", "merge"}},
		{name: "has messages error", cursor: 4, fail: "has", wantMerge: conversation.SequenceRange{Lo: 0, Hi: 5}, wantErrIs: failure, wantErr: "checking newly read feed messages: boom", wantSteps: []string{"cursor", "next", "merge", "has"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &readSpy{
				readingState: readingState{changed: tc.changed},
				t:            t,
				scope:        TopicScope{Scope: scope},
				joined:       joined,
				cursor:       tc.cursor,
				committed:    tc.committed,
				firstAfter:   tc.firstAfter,
				covered:      tc.covered,
				emptyCovered: tc.emptyCovered,
				fail:         tc.fail,
				failure:      failure,
			}
			writer := NewFeedWriter(
				func(platform.Tx) RangeWriter { return spy },
				func(platform.Tx) ChannelMessages { return spy },
				func(platform.Tx) EventCursor { return spy },
			)
			changed, err := writer.Read(t.Context(), platform.Tx{}, scope, joined, tc.cursor)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %t, want %t", changed, tc.wantChanged)
			}
			if !errors.Is(err, tc.wantErrIs) || (tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr)) {
				t.Fatalf("err = %v, want %q wrapping %v", err, tc.wantErr, tc.wantErrIs)
			}
			if !slices.Equal(spy.steps, tc.wantSteps) {
				t.Fatalf("steps = %v, want %v", spy.steps, tc.wantSteps)
			}
			if spy.merged != tc.wantMerge {
				t.Fatalf("merged = %+v, want %+v", spy.merged, tc.wantMerge)
			}
		})
	}
}

func TestTopicWriterRead(t *testing.T) {
	failure := errors.New("boom")
	scope := TopicScope{
		Scope:   Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}},
		TopicID: kernel.ID{4},
	}
	joined := int64(5)
	readRanges := []conversation.SequenceRange{{Lo: 3, Hi: 4}}
	for _, tc := range []struct {
		name        string
		cursor      int64
		committed   int64
		candidates  []conversation.SequenceRange
		changed     bool
		fail        string
		wantLos     []int64
		wantHis     []int64
		wantChanged bool
		wantErrIs   error
		wantErr     string
		wantSteps   []string
	}{
		{name: "negative cursor", cursor: -1, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor"},
		{name: "max int64 cursor", cursor: math.MaxInt64, committed: math.MaxInt64, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor"},
		{name: "cursor above committed", cursor: 11, committed: 10, wantErrIs: ErrInvalidCursor, wantErr: "invalid read cursor", wantSteps: []string{"cursor"}},
		{name: "single candidate mapped to add and marks changed", cursor: 0, changed: true, candidates: []conversation.SequenceRange{{Lo: 2, Hi: 4}}, wantLos: []int64{2}, wantHis: []int64{4}, wantChanged: true, wantSteps: []string{"cursor", "prepare", "state", "ranges", "add"}},
		{name: "candidates mapped to add and mark changed", cursor: 10, changed: true, candidates: []conversation.SequenceRange{{Lo: 2, Hi: 4}, {Lo: 7, Hi: 9}}, wantLos: []int64{2, 7}, wantHis: []int64{4, 9}, wantChanged: true, wantSteps: []string{"cursor", "prepare", "state", "ranges", "add"}},
		{name: "empty candidates still call add with cursor", cursor: 7, wantLos: []int64{}, wantHis: []int64{}, wantSteps: []string{"cursor", "prepare", "state", "ranges", "add"}},
		{name: "cursor limit error", cursor: 4, fail: "cursor", wantErrIs: failure, wantErr: "reading topic cursor limit: boom", wantSteps: []string{"cursor"}},
		{name: "prepare error", cursor: 4, fail: "prepare", wantErrIs: failure, wantErr: "preparing topic read: boom", wantSteps: []string{"cursor", "prepare"}},
		{name: "candidate ranges error", cursor: 4, fail: "ranges", wantErrIs: failure, wantErr: "finding topic read candidates: boom", wantSteps: []string{"cursor", "prepare", "state", "ranges"}},
		{name: "add error", cursor: 4, changed: true, fail: "add", wantLos: []int64{2}, wantHis: []int64{3}, wantErrIs: failure, wantErr: "reading topic: boom", wantSteps: []string{"cursor", "prepare", "state", "ranges", "add"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &readSpy{
				readingState: readingState{changed: tc.changed},
				t:            t,
				scope:        scope,
				joined:       joined,
				cursor:       tc.cursor,
				committed:    tc.committed,
				readRanges:   readRanges,
				candidates:   tc.candidates,
				fail:         tc.fail,
				failure:      failure,
			}
			writer := NewTopicWriter(
				func(platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error) {
					return func(ctx context.Context, got TopicScope, gotJoined int64) (PreparedTopic, error) {
						if ctx != t.Context() || got != scope || gotJoined != joined {
							t.Fatalf("prepare args = (%v, %+v, %d)", ctx, got, gotJoined)
						}
						if err := spy.step("prepare"); err != nil {
							return nil, err
						}
						return spy, nil
					}
				},
				func(platform.Tx) TopicMessages { return spy },
				func(platform.Tx) EventCursor { return spy },
			)
			changed, err := writer.Read(t.Context(), platform.Tx{}, scope, joined, tc.cursor)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %t, want %t", changed, tc.wantChanged)
			}
			if !errors.Is(err, tc.wantErrIs) || (tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr)) {
				t.Fatalf("err = %v, want %q wrapping %v", err, tc.wantErr, tc.wantErrIs)
			}
			if !slices.Equal(spy.steps, tc.wantSteps) {
				t.Fatalf("steps = %v, want %v", spy.steps, tc.wantSteps)
			}
			wantAddCalled := slices.Contains(tc.wantSteps, "add")
			if spy.addCalled != wantAddCalled || !slices.Equal(spy.addedLos, tc.wantLos) || !slices.Equal(spy.addedHis, tc.wantHis) || (wantAddCalled && spy.addedCursor != tc.cursor) {
				t.Fatalf("add called = %t (%v, %v, %d), want %t (%v, %v, %d)", spy.addCalled, spy.addedLos, spy.addedHis, spy.addedCursor, wantAddCalled, tc.wantLos, tc.wantHis, tc.cursor)
			}
		})
	}
}
