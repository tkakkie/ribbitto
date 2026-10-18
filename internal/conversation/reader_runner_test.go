package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type stoppedSnapshot struct{ err error }

func (r stoppedSnapshot) InSnapshot(context.Context, func(platform.Snapshot) error) error {
	return r.err
}

func TestReaderSnapshotRunners(t *testing.T) {
	ordinary, count := errors.New("ordinary snapshot"), errors.New("count snapshot")
	m, channel := org.Membership{}, kernel.ID{1}
	for _, tt := range []struct {
		name  string
		read  func(*conversation.Reader) error
		count bool
	}{
		{"page", func(r *conversation.Reader) error { _, err := r.Page(t.Context(), m, channel, nil, nil); return err }, true},
		{"around", func(r *conversation.Reader) error { _, err := r.Around(t.Context(), m, channel, nil, 1); return err }, true},
		{"overview", func(r *conversation.Reader) error {
			_, err := r.Overview(t.Context(), m, channel, nil, nil, 0)
			return err
		}, true},
		{"sidebar", func(r *conversation.Reader) error { _, err := r.Sidebar(t.Context(), m, channel, nil); return err }, true},
		{"one", func(r *conversation.Reader) error { _, err := r.One(t.Context(), m, channel, 1); return err }, false},
		{"many", func(r *conversation.Reader) error {
			_, err := r.Many(t.Context(), m, channel, []kernel.ID{{2}})
			return err
		}, false},
		{"locate", func(r *conversation.Reader) error { _, err := r.Locate(t.Context(), m, kernel.ID{2}); return err }, false},
		{"pins", func(r *conversation.Reader) error { _, err := r.ReadPins(t.Context(), m, channel); return err }, false},
		{"members", func(r *conversation.Reader) error { _, err := r.Members(t.Context(), m, channel, nil); return err }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, counts := range []conversation.PageCountsIn{nil, func(platform.Snapshot) conversation.PageCounter { return nil }} {
				r := conversation.NewReader(stoppedSnapshot{ordinary}, stoppedSnapshot{count}, nil, nil, nil, nil, counts)
				want := ordinary
				if tt.count && counts != nil {
					want = count
				}
				if err := tt.read(r); !errors.Is(err, want) {
					t.Fatalf("read error = %v, want %v (counts=%t)", err, want, counts != nil)
				}
			}
		})
	}
}

func TestReaderPageWithoutCounts(t *testing.T) {
	data := fullHistory{n: 1}
	_, f := testReader(t, data)
	r := conversation.NewReader(f, nil,
		func(platform.Snapshot) conversation.ReadStore { return f },
		func(platform.Snapshot) conversation.MemberDirectory { return data },
		func(platform.Snapshot) conversation.AccountDirectory { return data },
		func(platform.Snapshot) conversation.EventCursor { return f }, nil)
	page, err := r.Page(t.Context(), org.Membership{}, kernel.ID{1}, nil, nil)
	if err != nil || page.EventCursor == nil || *page.EventCursor != 7 || len(page.Entries) == 0 ||
		page.ChannelCounts != nil || page.TopicCounts != nil {
		t.Fatalf("count-free page = %+v, %v; want history and cursor without counts", page, err)
	}
}
