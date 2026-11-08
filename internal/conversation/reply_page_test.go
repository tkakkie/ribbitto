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

type replyPageFake struct {
	*snapshotFake
	channelErr, pageErr error
	m                   org.Membership
	before              *int64
	rows                []conversation.Message
}

func (f replyPageFake) ReadableChannelIDs(_ context.Context, m org.Membership) ([]kernel.ID, error) {
	if m != f.m {
		f.t.Fatal("wrong read-access membership")
	}
	return []kernel.ID{{3}}, f.channelErr
}

func (f replyPageFake) ListReplies(_ context.Context, organizationID, memberID kernel.ID, ids []kernel.ID, before *int64) ([]conversation.Message, error) {
	if f.channelErr != nil || organizationID != f.m.Organization.ID || memberID != f.m.Member.ID || !reflect.DeepEqual(ids, []kernel.ID{{3}}) || before != f.before {
		f.t.Fatal("page read after access failure or with wrong scope/cursor")
	}
	return f.rows, f.pageErr
}

func TestReplyPageFailuresAndScope(t *testing.T) {
	failure := errors.New("failed")
	for _, stage := range []string{"empty", "50", "51", "channels", "page", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			_, snapshot := testReader(t, fullHistory{})
			f := replyPageFake{snapshotFake: snapshot, m: org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}, before: new(int64(9))}
			count := 0
			if stage == "50" {
				count = 50
			}
			if stage == "51" {
				count = 51
			}
			for i := range count {
				f.rows = append(f.rows, conversation.Message{EventSeq: int64(100 - i)})
			}
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
			reader := conversation.NewReplies(runner, func(s platform.Snapshot) conversation.ReplyReader {
				snapshot.bound(s, "replies")
				return f
			}, func(s platform.Snapshot) conversation.ReadableChannels {
				snapshot.bound(s, "readable")
				return f
			})
			page, err := reader.Page(t.Context(), f.m, f.before)
			if stage == "empty" || count > 0 {
				if err != nil || !reflect.DeepEqual(page.Messages, f.rows[:min(count, 50)]) || (page.Next != nil) != (count > 50) || (page.Next != nil && *page.Next != 51) {
					t.Fatalf("page=%+v error=%v", page, err)
				}
			} else if !errors.Is(err, failure) || !reflect.DeepEqual(page, conversation.ReplyPage{}) {
				t.Fatalf("failure returned page=%+v error=%v", page, err)
			}
		})
	}
}
