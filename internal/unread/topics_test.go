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

type fakeTopicStateReader struct {
	state    TopicReadState
	err      error
	scope    Scope
	joined   int64
	listed   []kernel.ID
	selected *kernel.ID
}

func (f *fakeTopicStateReader) Read(_ context.Context, scope Scope, joined int64, listed []kernel.ID, selected *kernel.ID) (TopicReadState, error) {
	f.scope, f.joined, f.listed, f.selected = scope, joined, slices.Clone(listed), selected
	return f.state, f.err
}

type fakeTopicCounter struct {
	counts        map[kernel.ID]int64
	first, prefix int64
	err           error
	called        bool
	org, channel  kernel.ID
	ids           []kernel.ID
	floors        []int64
	read          []conversation.SequenceRange
	selected      *kernel.ID
	firstUnread   bool
}

func (f *fakeTopicCounter) CountWithFirstUnread(_ context.Context, org, channel kernel.ID, prefix int64, ids []kernel.ID, floors []int64, read []conversation.SequenceRange, selected *kernel.ID, firstUnread bool) (map[kernel.ID]int64, int64, error) {
	f.called, f.org, f.channel, f.prefix = true, org, channel, prefix
	f.ids, f.floors, f.read = slices.Clone(ids), slices.Clone(floors), slices.Clone(read)
	f.selected, f.firstUnread = selected, firstUnread
	return f.counts, f.first, f.err
}

func TestTopicCounts(t *testing.T) {
	failure := errors.New("boom")
	scope := Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}
	joined := int64(4)
	firstTopic, secondTopic := kernel.ID{10}, kernel.ID{20}
	listed := []kernel.ID{firstTopic, secondTopic}
	state := TopicReadState{
		Prefix: 7,
		Ranges: []conversation.SequenceRange{{Lo: 8, Hi: 11}, {Lo: 15, Hi: 19}},
		Floors: map[kernel.ID]int64{firstTopic: 6, secondTopic: 14},
	}
	wantRead := []conversation.SequenceRange{{Lo: 8, Hi: 11}, {Lo: 15, Hi: 19}}
	wantCounts := map[kernel.ID]int64{firstTopic: 3, secondTopic: 1}
	wantFirst := int64(16)

	for _, tc := range []struct {
		name            string
		selected        *kernel.ID
		firstUnread     bool
		wantFirstUnread bool
		stateErr        error
		counterErr      error
		wantErr         string
	}{
		{name: "ReadWithFirstUnread true", selected: &secondTopic, firstUnread: true, wantFirstUnread: true},
		{name: "ReadWithFirstUnread false without selected", selected: nil, firstUnread: false, wantFirstUnread: false},
		{name: "state error", selected: &firstTopic, firstUnread: true, stateErr: failure, wantErr: "loading topic count state: "},
		{name: "counter error", selected: &firstTopic, firstUnread: true, wantFirstUnread: true, counterErr: failure, wantErr: "reading topic counts: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeTopicStateReader{state: state, err: tc.stateErr}
			counter := &fakeTopicCounter{counts: wantCounts, first: wantFirst, err: tc.counterErr}
			counts := NewTopicCounts(func(platform.Snapshot) TopicStateReader { return reader }, func(platform.Snapshot) TopicCounter { return counter })
			gotCounts, gotFirst, err := counts.ReadWithFirstUnread(t.Context(), platform.Snapshot{}, scope, joined, listed, tc.selected, tc.firstUnread)
			if reader.scope != scope || reader.joined != joined || !slices.Equal(reader.listed, listed) || reader.selected != tc.selected {
				t.Fatalf("reader args=(%+v, %d, %v, %v), want (%+v, %d, %v, %v)", reader.scope, reader.joined, reader.listed, reader.selected, scope, joined, listed, tc.selected)
			}
			if tc.wantErr != "" {
				if err == nil || !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v, want %q wrapping %v", err, tc.wantErr, failure)
				}
				if gotCounts != nil || gotFirst != 0 || counter.called != (tc.counterErr != nil) {
					t.Fatalf("got=(%v, %d) counter.called=%t on error", gotCounts, gotFirst, counter.called)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !counter.called || counter.org != scope.OrganizationID || counter.channel != scope.ChannelID || counter.prefix != state.Prefix {
				t.Fatalf("counter scope=(%v, %v, %d), want (%v, %v, %d)", counter.org, counter.channel, counter.prefix, scope.OrganizationID, scope.ChannelID, state.Prefix)
			}
			gotFloors := make(map[kernel.ID]int64, len(counter.ids))
			if len(counter.ids) == len(counter.floors) {
				for i, id := range counter.ids {
					gotFloors[id] = counter.floors[i]
				}
			}
			if len(counter.ids) != len(counter.floors) || len(gotFloors) != len(counter.ids) || !maps.Equal(gotFloors, state.Floors) {
				t.Fatalf("floors ids=%v floors=%v, want %v", counter.ids, counter.floors, state.Floors)
			}
			if !slices.Equal(counter.read, wantRead) {
				t.Fatalf("read ranges=%v, want %v", counter.read, wantRead)
			}
			if counter.selected != tc.selected || counter.firstUnread != tc.wantFirstUnread {
				t.Fatalf("selected=%v firstUnread=%t, want %v %t", counter.selected, counter.firstUnread, tc.selected, tc.wantFirstUnread)
			}
			if !maps.Equal(gotCounts, wantCounts) || gotFirst != wantFirst {
				t.Fatalf("got=(%v, %d), want (%v, %d)", gotCounts, gotFirst, wantCounts, wantFirst)
			}
		})
	}
}
