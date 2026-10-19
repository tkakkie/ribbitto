package unread

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type postState struct {
	readingState
	merge func(Scope, int64, conversation.SequenceRange) error
	add   func([]int64, []int64, int64) error
}

func (p postState) Merge(_ context.Context, s Scope, j int64, r conversation.SequenceRange) error {
	return p.merge(s, j, r)
}
func (p postState) Add(_ context.Context, lo, hi []int64, floor int64) error {
	return p.add(lo, hi, floor)
}

func TestPostReads(t *testing.T) {
	failure := errors.New("failed step")
	scope := Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}
	topic, cursor := kernel.ID{4}, int64(10)
	for _, tc := range []struct {
		name   string
		cursor *int64
		topic  *kernel.ID
		fail   string
		steps  []string
	}{
		{"no cursor", nil, nil, "", []string{"merge"}},
		{"no cursor with topic", nil, &topic, "", []string{"merge"}},
		{"feed", &cursor, nil, "", []string{"feed", "merge"}},
		{"topic", &cursor, &topic, "", []string{"topic", "prepare", "add"}},
		{"own merge error", nil, nil, "merge", []string{"merge"}},
		{"feed error", &cursor, nil, "feed", []string{"feed"}},
		{"feed merge error", &cursor, nil, "merge", []string{"feed", "merge"}},
		{"topic error", &cursor, &topic, "topic", []string{"topic"}},
		{"prepare error", &cursor, &topic, "prepare", []string{"topic", "prepare"}},
		{"own add error", &cursor, &topic, "add", []string{"topic", "prepare", "add"}},
		{"unprepared", &cursor, &topic, "unprepared", []string{"topic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			step := func(name string) error {
				steps = append(steps, name)
				if tc.fail == name {
					return failure
				}
				return nil
			}
			state := postState{merge: func(s Scope, j int64, r conversation.SequenceRange) error {
				if s != scope || j != 5 || r != (conversation.SequenceRange{Lo: 11, Hi: 12}) {
					t.Fatal("incorrect merge arguments")
				}
				return step("merge")
			}, add: func(lo, hi []int64, floor int64) error {
				if !reflect.DeepEqual(lo, []int64{11}) || !reflect.DeepEqual(hi, []int64{12}) || floor != 9 {
					t.Fatal("incorrect add arguments")
				}
				return step("add")
			}}
			reads := NewPostReads(func(platform.Tx) RangeWriter { return state },
				func(tx platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error) {
					return func(ctx context.Context, s TopicScope, j int64) (PreparedTopic, error) {
						if s.Scope != scope || j != 5 || s.TopicID != topic {
							t.Fatal("incorrect prepared topic")
						}
						return state, step("prepare")
					}
				}, func() FeedRead {
					return FeedRead(func(ctx context.Context, tx platform.Tx, s Scope, j, c int64) (bool, error) {
						if s != scope || j != 5 || c != cursor {
							t.Fatal("incorrect feed cursor")
						}
						return false, step("feed")
					})
				},
				func(prepare TopicPrepareIn) TopicRead {
					return TopicRead(func(ctx context.Context, tx platform.Tx, s TopicScope, j, c int64) (bool, error) {
						if s.Scope != scope || j != 5 || s.TopicID != topic || c != cursor {
							t.Fatal("incorrect topic arguments")
						}
						if err := step("topic"); err != nil {
							return false, err
						}
						if tc.fail == "unprepared" {
							return false, nil
						}
						_, err := prepare(tx)(ctx, s, j)
						return false, err
					})
				})
			err := reads.Read(t.Context(), platform.Tx{}, scope, 5, tc.topic, tc.cursor, conversation.SequenceRange{Lo: 11, Hi: 12}, 9)
			if tc.fail == "unprepared" {
				if err == nil || err.Error() != "posting topic read: page write returned without preparing" {
					t.Fatalf("guard error = %v", err)
				}
			} else if !errors.Is(err, failure) && tc.fail != "" || err != nil && tc.fail == "" {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("steps = %v, want %v", steps, tc.steps)
			}
		})
	}
}
