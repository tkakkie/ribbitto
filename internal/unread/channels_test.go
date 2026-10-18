package unread

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type fakeChannelRanges struct {
	states      map[kernel.ID]ChannelReadState
	err         error
	org, member kernel.ID
	joined      int64
	ids         []kernel.ID
}

func (f *fakeChannelRanges) Read(_ context.Context, org, member kernel.ID, joined int64, ids []kernel.ID) (map[kernel.ID]ChannelReadState, error) {
	f.org, f.member, f.joined, f.ids = org, member, joined, slices.Clone(ids)
	return f.states, f.err
}

type fakeChannelCounter struct {
	counts, first    map[kernel.ID]int64
	err              error
	called           bool
	org              kernel.ID
	ids, gapChannels []kernel.ID
	los, his         []int64
	firstUnread      bool
}

func (f *fakeChannelCounter) CountWithFirstUnread(_ context.Context, org kernel.ID, ids, gapChannels []kernel.ID, los, his []int64, firstUnread bool) (map[kernel.ID]int64, map[kernel.ID]int64, error) {
	f.called, f.org, f.firstUnread = true, org, firstUnread
	f.ids, f.gapChannels = slices.Clone(ids), slices.Clone(gapChannels)
	f.los, f.his = slices.Clone(los), slices.Clone(his)
	return f.counts, f.first, f.err
}

func TestChannelCounts(t *testing.T) {
	failure := errors.New("boom")
	org, member := kernel.ID{1}, kernel.ID{2}
	firstCh, secondCh, thirdCh := kernel.ID{10}, kernel.ID{20}, kernel.ID{30}
	joined := int64(5)
	channels := []kernel.ID{firstCh, secondCh, firstCh, thirdCh, secondCh}
	wantIDs := []kernel.ID{firstCh, secondCh, thirdCh}
	states := map[kernel.ID]ChannelReadState{
		firstCh:  {Prefix: 5, Gaps: []conversation.SequenceRange{{Lo: 6, Hi: 8}, {Lo: 12, Hi: 15}}},
		secondCh: {Prefix: 5},
		thirdCh:  {Prefix: 9, Gaps: []conversation.SequenceRange{{Lo: 10, Hi: 20}}},
	}
	wantGapChannels := []kernel.ID{firstCh, firstCh, thirdCh}
	wantLos := []int64{6, 12, 10}
	wantHis := []int64{8, 15, 20}
	wantCounts := map[kernel.ID]ChannelCount{
		firstCh:  {Count: 4, FirstUnread: 6},
		secondCh: {Count: 0, FirstUnread: 0},
		thirdCh:  {Count: 1, FirstUnread: 0},
	}

	for _, tc := range []struct {
		name            string
		firstUnread     bool
		wantFirstUnread bool
		rangesErr       error
		counterErr      error
		wantErr         string
	}{
		{name: "ReadWithFirstUnread true", firstUnread: true, wantFirstUnread: true},
		{name: "ReadWithFirstUnread false", firstUnread: false, wantFirstUnread: false},
		{name: "ranges error", firstUnread: true, rangesErr: failure, wantErr: "loading channel count ranges: "},
		{name: "counter error", firstUnread: true, wantFirstUnread: true, counterErr: failure, wantErr: "reading channel counts: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ranges := &fakeChannelRanges{states: states, err: tc.rangesErr}
			counter := &fakeChannelCounter{counts: map[kernel.ID]int64{firstCh: 4, thirdCh: 1}, first: map[kernel.ID]int64{firstCh: 6}, err: tc.counterErr}
			counts := NewChannelCounts(func(platform.Snapshot) ChannelRanges { return ranges }, func(platform.Snapshot) ChannelCounter { return counter })
			got, err := counts.ReadWithFirstUnread(t.Context(), platform.Snapshot{}, org, member, joined, channels, tc.firstUnread)
			if ranges.org != org || ranges.member != member || ranges.joined != joined || !slices.Equal(ranges.ids, wantIDs) {
				t.Fatalf("ranges args=(%v, %v, %d, %v), want (%v, %v, %d, %v)", ranges.org, ranges.member, ranges.joined, ranges.ids, org, member, joined, wantIDs)
			}
			if tc.wantErr != "" {
				if err == nil || !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v, want %q wrapping %v", err, tc.wantErr, failure)
				}
				if got != nil || counter.called != (tc.counterErr != nil) {
					t.Fatalf("got=%v counter.called=%t on error", got, counter.called)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !counter.called || counter.org != org || !slices.Equal(counter.ids, wantIDs) {
				t.Fatalf("counter org=%v ids=%v, want org=%v ids=%v", counter.org, counter.ids, org, wantIDs)
			}
			if !slices.Equal(counter.gapChannels, wantGapChannels) || !slices.Equal(counter.los, wantLos) || !slices.Equal(counter.his, wantHis) {
				t.Fatalf("gaps=(%v, %v, %v), want (%v, %v, %v)", counter.gapChannels, counter.los, counter.his, wantGapChannels, wantLos, wantHis)
			}
			if counter.firstUnread != tc.wantFirstUnread {
				t.Fatalf("firstUnread=%t, want %t", counter.firstUnread, tc.wantFirstUnread)
			}
			if !maps.Equal(got, wantCounts) {
				t.Fatalf("got=%v, want %v", got, wantCounts)
			}
		})
	}
}
