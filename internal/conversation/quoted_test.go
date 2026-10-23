package conversation_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestReaderQuoted(t *testing.T) {
	original := conversation.Message{ID: kernel.ID{4}, OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, TopicID: kernel.ID{7}, MemberID: kernel.ID{3}, Body: "original", EventSeq: 9}
	failure := errors.New("store failed")
	for _, tt := range []struct {
		name    string
		found   []conversation.Message
		readErr error
		wantErr bool
	}{
		{name: "found", found: []conversation.Message{original}},
		{name: "missing or outside the channel"},
		{name: "store failure", readErr: failure, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &historyPathsBatch{
				partialBatch: partialBatch{t: t, found: tt.found},
				readErr:      tt.readErr,
				members:      map[kernel.ID]org.DirectoryEntry{original.MemberID: {AccountID: kernel.ID{6}, Handle: "alice"}},
				names:        map[kernel.ID]string{{6}: "Alice"},
				topics:       map[kernel.ID]conversation.Topic{original.TopicID: {ID: original.TopicID}},
			}
			reader, _ := testReader(t, f)
			membership := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
			got, err := reader.Quoted(t.Context(), membership, original.ChannelID, original.ID)
			if tt.found == nil {
				tt.wantErr = true
			}
			if tt.wantErr {
				var quoteErr *conversation.QuoteReadError
				if err == nil || !errors.As(err, &quoteErr) || quoteErr.MessageID != original.ID {
					t.Fatalf("Quoted = %+v, %v; want an error that names message %x", got, err, original.ID)
				}
				if got.ID != (kernel.ID{}) {
					t.Fatalf("Quoted returned an entry with its error: %+v", got)
				}
				return
			}
			if err != nil || got.ID != original.ID || got.Handle != "alice" || got.DisplayName != "Alice" {
				t.Fatalf("Quoted = %+v, %v; want the original by alice", got, err)
			}
		})
	}
}
