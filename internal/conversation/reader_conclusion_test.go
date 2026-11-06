package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

type conclusionHistory struct {
	fullHistory
	snapshot *snapshotFake
	message  conversation.Message
	calls    int
	missing  bool
}

func (f *conclusionHistory) GetMessages(_ context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) ([]conversation.Message, error) {
	f.snapshot.t.Helper()
	if !f.snapshot.active || organizationID != f.message.OrganizationID || channelID != f.message.ChannelID || !reflect.DeepEqual(ids, []kernel.ID{f.message.ID}) {
		f.snapshot.t.Fatal("conclusion read outside the page snapshot or wrong scope/ID")
	}
	f.calls++
	if f.missing {
		return nil, nil
	}
	return []conversation.Message{f.message}, nil
}

func (f *conclusionHistory) LookupTopics(context.Context, kernel.ID, kernel.ID, []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	return map[kernel.ID]conversation.Topic{f.message.TopicID: {ID: f.message.TopicID}}, nil
}

func TestReaderConclusion(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		selected, concluded, missing bool
	}{
		{"feed", false, false, false},
		{"open topic", true, false, false},
		{"conclusion", true, true, false},
		{"missing conclusion", true, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}
			topic := conversation.Topic{ID: kernel.ID{7}, ChannelID: kernel.ID{2}}
			data := &conclusionHistory{message: conversation.Message{ID: kernel.ID{8}, OrganizationID: m.Organization.ID, ChannelID: topic.ChannelID, TopicID: topic.ID, Body: "Decision", MemberID: kernel.ID{3}}, missing: tt.missing}
			reader, snapshot := testReader(t, data)
			data.snapshot, snapshot.organizationID = snapshot, m.Organization.ID
			snapshot.selected = &topic
			var topicID *kernel.ID
			if tt.selected {
				topicID = &topic.ID
			}
			if tt.concluded {
				topic.ConclusionMessageID = data.message.ID
			}
			page, err := reader.Page(t.Context(), m, topic.ChannelID, topicID, nil)
			if tt.missing {
				if !errors.Is(err, conversation.ErrMessageNotFound) {
					t.Fatalf("missing conclusion: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.concluded {
				if data.calls != 1 || page.Conclusion == nil || page.Conclusion.Message != data.message || page.Conclusion.TopicID != page.Topic.ID || page.Conclusion.DisplayName != "Author" {
					t.Fatalf("conclusion = %+v, reads = %d", page.Conclusion, data.calls)
				}
			} else if page.Conclusion != nil || data.calls != 0 {
				t.Fatalf("absent conclusion = %+v, reads = %d", page.Conclusion, data.calls)
			}
		})
	}
}
