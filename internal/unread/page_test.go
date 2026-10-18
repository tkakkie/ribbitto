package unread

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type pageChannels func(context.Context, platform.Snapshot, kernel.ID, kernel.ID, int64, []kernel.ID, bool) (map[kernel.ID]ChannelCount, error)

func (f pageChannels) ReadWithFirstUnread(ctx context.Context, s platform.Snapshot, o, m kernel.ID, j int64, ids []kernel.ID, first bool) (map[kernel.ID]ChannelCount, error) {
	return f(ctx, s, o, m, j, ids, first)
}

type pageTopics func(context.Context, platform.Snapshot, Scope, int64, []kernel.ID, *kernel.ID, bool) (map[kernel.ID]int64, int64, error)

func (f pageTopics) ReadWithFirstUnread(ctx context.Context, s platform.Snapshot, scope Scope, j int64, ids []kernel.ID, selected *kernel.ID, first bool) (map[kernel.ID]int64, int64, error) {
	return f(ctx, s, scope, j, ids, selected, first)
}

func TestPageCounts(t *testing.T) {
	failure := errors.New("count failed")
	scope := Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}
	other, topic := kernel.ID{4}, kernel.ID{5}
	channels, topics := []kernel.ID{other, scope.ChannelID}, []kernel.ID{topic}
	for _, tc := range []struct {
		name     string
		first    bool
		selected *kernel.ID
		fail     string
	}{
		{"first unread", true, &topic, ""}, {"counts only", false, &topic, ""}, {"no selection", true, nil, ""},
		{"channel error", true, &topic, "channels"}, {"topic error", true, &topic, "topics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			counts := NewPageCounts(func() PageChannelCounts {
				return pageChannels(func(ctx context.Context, s platform.Snapshot, o, m kernel.ID, j int64, ids []kernel.ID, first bool) (map[kernel.ID]ChannelCount, error) {
					steps = append(steps, "channels")
					if ctx != t.Context() || s != (platform.Snapshot{}) || o != scope.OrganizationID || m != scope.MemberID || j != 6 || !reflect.DeepEqual(ids, channels) || first != tc.first {
						t.Fatal("incorrect channel arguments")
					}
					if tc.fail == "channels" {
						return nil, failure
					}
					return map[kernel.ID]ChannelCount{other: {Count: 7, FirstUnread: 8}, scope.ChannelID: {Count: 9, FirstUnread: 10}}, nil
				})
			}, func() PageTopicCounts {
				return pageTopics(func(ctx context.Context, s platform.Snapshot, got Scope, j int64, ids []kernel.ID, selected *kernel.ID, first bool) (map[kernel.ID]int64, int64, error) {
					steps = append(steps, "topics")
					if ctx != t.Context() || s != (platform.Snapshot{}) || got != scope || j != 6 || !reflect.DeepEqual(ids, topics) || selected != tc.selected || first != tc.first {
						t.Fatal("incorrect topic arguments")
					}
					if tc.fail == "topics" {
						return nil, 0, failure
					}
					return map[kernel.ID]int64{topic: 11}, 12, nil
				})
			})
			got, err := counts.Read(t.Context(), platform.Snapshot{}, scope, 6, channels, topics, tc.selected, tc.first)
			want := PageCountResult{ChannelCounts: map[kernel.ID]int64{other: 7, scope.ChannelID: 9}, FeedFirstUnread: 10, TopicCounts: map[kernel.ID]int64{topic: 11}, TopicFirstUnread: 12}
			wantSteps := []string{"channels", "topics"}
			switch tc.fail {
			case "channels":
				want = PageCountResult{}
				wantSteps = []string{"channels"}
			case "topics":
				want.TopicCounts, want.TopicFirstUnread = nil, 0
			}
			if errors.Is(err, failure) != (tc.fail != "") || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(steps, wantSteps) {
				t.Fatalf("got (%+v,%v,%v), want (%+v,%s,%v)", got, err, steps, want, tc.fail, wantSteps)
			}
		})
	}
}
