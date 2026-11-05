package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type mentionPageFake struct {
	*snapshotFake
	channelErr, pageErr error
	m                   org.Membership
	before              *int64
}

func (f mentionPageFake) ReadableChannelIDs(_ context.Context, m org.Membership) ([]kernel.ID, error) {
	if m != f.m {
		f.t.Fatal("wrong read-access membership")
	}
	return []kernel.ID{{3}}, f.channelErr
}

func (f mentionPageFake) ListMentions(_ context.Context, organizationID, memberID kernel.ID, ids []kernel.ID, before *int64) ([]conversation.Message, error) {
	if f.channelErr != nil || organizationID != f.m.Organization.ID || memberID != f.m.Member.ID || !reflect.DeepEqual(ids, []kernel.ID{{3}}) || before != f.before {
		f.t.Fatal("page read after access failure or with wrong scope/cursor")
	}
	return []conversation.Message{{EventSeq: 1}}, f.pageErr
}

func TestMentionPageFailuresAndScope(t *testing.T) {
	failure := errors.New("failed")
	for _, stage := range []string{"success", "channels", "page", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			_, snapshot := testReader(t, fullHistory{})
			f := mentionPageFake{snapshotFake: snapshot, m: org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}, before: new(int64(9))}
			if stage == "channels" {
				f.channelErr = failure
			}
			if stage == "page" {
				f.pageErr = failure
			}
			var runner conversation.SnapshotRunner = snapshot
			if stage == "snapshot" {
				runner = stoppedSnapshot{err: failure}
			}
			reader := conversation.NewMentions(runner, func(s platform.Snapshot) conversation.MentionReader {
				snapshot.bound(s, "mentions")
				return f
			}, func(s platform.Snapshot) conversation.ReadableChannels {
				snapshot.bound(s, "readable")
				return f
			})
			page, err := reader.Page(t.Context(), f.m, f.before)
			if stage == "success" {
				if err != nil || len(page.Messages) != 1 || page.Messages[0].EventSeq != 1 || page.Next != nil {
					t.Fatalf("page=%+v error=%v", page, err)
				}
			} else if !errors.Is(err, failure) || !reflect.DeepEqual(page, conversation.MentionPage{}) {
				t.Fatalf("failure returned page=%+v error=%v", page, err)
			}
		})
	}
}
