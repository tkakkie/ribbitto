package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func (f fullHistory) ListMessagesAfter(_ context.Context, _, _ kernel.ID, _ *kernel.ID, after int64, limit int32) ([]conversation.Message, error) {
	var out []conversation.Message
	for seq := max(after+1, 1); seq <= int64(f.n) && len(out) < int(limit); seq++ {
		out = append(out, conversation.Message{ID: kernel.ID{byte(seq)}, EventSeq: seq, MemberID: kernel.ID{3}})
	}
	return out, nil
}

// located is a history whose LocateMessage answers newer, or err.
type located struct {
	fullHistory
	newer int
	err   error
}

func (f *snapshotFake) ListMessagesAfter(ctx context.Context, orgID, ch kernel.ID, topic *kernel.ID, after int64, limit int32) ([]conversation.Message, error) {
	if err := f.step("history after"); err != nil {
		return nil, err
	}
	return f.historyData.(fullHistory).ListMessagesAfter(ctx, orgID, ch, topic, after, limit)
}

func (f *snapshotFake) LocateMessage(_ context.Context, orgID, id kernel.ID, limit int32) (conversation.Location, int, error) {
	if orgID != f.organizationID || id != (kernel.ID{9}) || limit != conversation.PageSize {
		f.t.Fatalf("LocateMessage(%x, %x, %d): wrong scope or limit", orgID, id, limit)
	}
	data := f.historyData.(located)
	return conversation.Location{ChannelID: kernel.ID{2}, TopicID: kernel.ID{7}, EventSeq: 90}, data.newer, data.err
}

// An around page holds up to half a page from the position on and fills
// the rest with older messages; Older says whether more are left before it.
func TestPageAround(t *testing.T) {
	for _, tt := range []struct {
		messages       int
		around         int64
		oldest, newest int64
		older          bool
	}{
		{200, 100, 75, 124, true},
		{200, 190, 151, 200, true},
		{200, 30, 5, 54, true},
		{200, 26, 1, 50, false},
		{200, 10, 1, 34, false},
		{40, 20, 1, 40, false},
	} {
		reader, f := testReader(t, fullHistory{tt.messages})
		page, err := reader.Around(t.Context(), org.Membership{}, kernel.ID{2}, &kernel.ID{7}, tt.around)
		if err != nil {
			t.Fatal(err)
		}
		n := len(page.Entries)
		if n == 0 || page.Entries[0].EventSeq != tt.oldest || page.Entries[n-1].EventSeq != tt.newest || n != int(tt.newest-tt.oldest+1) || page.Older != tt.older {
			t.Errorf("%d messages around %d: %d entries, older %v, calls %v; want %d…%d, older %v", tt.messages, tt.around, n, page.Older, f.calls, tt.oldest, tt.newest, tt.older)
			continue
		}
		for i := 1; i < n; i++ {
			if page.Entries[i].EventSeq != page.Entries[i-1].EventSeq+1 {
				t.Fatalf("%d messages around %d: not oldest first", tt.messages, tt.around)
			}
		}
	}
}

// A message is on its topic's latest page while fewer than PageSize newer
// messages follow it there; a missing one stays ErrMessageNotFound.
func TestReaderLocate(t *testing.T) {
	membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
	for _, tt := range []struct {
		data   located
		latest bool
	}{
		{located{newer: 0}, true},
		{located{newer: conversation.PageSize - 1}, true},
		{located{newer: conversation.PageSize}, false},
		{located{err: conversation.ErrMessageNotFound}, false},
	} {
		reader, f := testReader(t, tt.data)
		f.organizationID = membership.Organization.ID
		at, err := reader.Locate(t.Context(), membership, kernel.ID{9})
		if !errors.Is(err, tt.data.err) || err == nil && (at.Latest != tt.latest || at.ChannelID != (kernel.ID{2}) || at.TopicID != (kernel.ID{7}) || at.EventSeq != 90) {
			t.Errorf("newer %d: Locate = %+v, %v; want latest %v, error %v", tt.data.newer, at, err, tt.latest, tt.data.err)
		}
	}
}
