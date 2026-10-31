package conversation_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestReaderOverviewAround(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	pinnedAt := time.Unix(5, 0)
	pin := conversation.PinnedMessage{
		Message:  conversation.Message{ID: kernel.ID{8}, MemberID: kernel.ID{3}, Body: " a\n b "},
		PinState: conversation.PinState{PinnedByMemberID: kernel.ID{3}, PinnedAt: pinnedAt},
	}
	wantPins := []conversation.Pin{{
		Entry:      conversation.Entry{Message: pin.Message, DisplayName: "Author"},
		PinnerName: "Author",
		PinnedAt:   pinnedAt,
	}}
	head := []string{"snapshot", "reads", "channel", "channels", "topics", "members", "accounts"}
	tail := []string{"pins", "members", "accounts", "bind cursor", "cursor", "bind counts", "counts"}
	beforeCalls := slices.Concat(head, []string{"history"}, tail)
	aroundCalls := slices.Concat(head, []string{"history after", "history"}, tail)
	before50 := int64(50)

	for _, tt := range []struct {
		name           string
		around         int64
		before         *int64
		fail           string
		oldest, newest int64
		older          bool
		calls          []string
	}{
		{name: "around 100", around: 100, oldest: 75, newest: 124, older: true, calls: aroundCalls},
		{name: "around 1", around: 1, oldest: 1, newest: 25, older: false, calls: aroundCalls},
		{name: "around 0, before nil", around: 0, oldest: 151, newest: 200, older: true, calls: beforeCalls},
		{name: "around -5", around: -5, oldest: 151, newest: 200, older: true, calls: beforeCalls},
		{name: "around 100, before 50", around: 100, before: &before50, oldest: 75, newest: 124, older: true, calls: aroundCalls},
		{name: "around 0, before 50", around: 0, before: &before50, oldest: 1, newest: 49, older: false, calls: beforeCalls},
		{name: "newer half fails", around: 100, fail: "history after", calls: slices.Concat(head, []string{"history after"})},
		{name: "older half fails", around: 100, fail: "history", calls: slices.Concat(head, []string{"history after", "history"})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader, f := testReader(t, fullHistory{200})
			f.fail, f.organizationID = tt.fail, membership.Organization.ID
			f.pins = []conversation.PinnedMessage{pin}
			page, err := reader.Overview(t.Context(), membership, kernel.ID{2}, nil, tt.before, tt.around)
			if !reflect.DeepEqual(f.calls, tt.calls) {
				t.Errorf("%s: calls = %v, want %v", tt.name, f.calls, tt.calls)
			}
			if tt.fail != "" {
				if !errors.Is(err, f.err) {
					t.Errorf("%s: error = %v, want %v", tt.name, err, f.err)
				}
				if len(page.Pins) != 0 {
					t.Errorf("%s: Pins = %+v, want none", tt.name, page.Pins)
				}
				if len(page.Entries) != 0 {
					t.Errorf("%s: Entries = %+v, want none", tt.name, page.Entries)
				}
				return
			}
			if err != nil {
				t.Errorf("%s: Overview error = %v, want nil", tt.name, err)
			}
			var gotSeqs, wantSeqs []int64
			for _, e := range page.Entries {
				gotSeqs = append(gotSeqs, e.EventSeq)
			}
			for seq := tt.oldest; seq <= tt.newest; seq++ {
				wantSeqs = append(wantSeqs, seq)
			}
			if !slices.Equal(gotSeqs, wantSeqs) {
				t.Errorf("%s: entry sequences = %v, want %d..%d", tt.name, gotSeqs, tt.oldest, tt.newest)
			}
			if page.Older != tt.older {
				t.Errorf("%s: Older = %v, want %v", tt.name, page.Older, tt.older)
			}
			if !reflect.DeepEqual(page.Pins, wantPins) {
				t.Errorf("%s: Pins = %+v, want %+v", tt.name, page.Pins, wantPins)
			}
			if page.EventCursor == nil || *page.EventCursor != 7 {
				t.Errorf("%s: EventCursor = %v, want 7", tt.name, page.EventCursor)
			}
		})
	}
}
