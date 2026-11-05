package conversationpg_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestResolutionMultiMessageBranch(t *testing.T) {
	for _, tc := range []struct {
		name               string
		includesConclusion bool
	}{
		{"conclusion selected after first message", true},
		{"conclusion not selected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := pgtest.New(t)
			ctx := t.Context()
			f := conversationtest.OrganizationWithOwner(t, pool, "multi-message", "general")
			m := membership(f.OrganizationID, f.MemberID)
			m.Member.OrganizationID = f.OrganizationID
			source := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "decision").ID
			destination := f.Channel.DefaultTopicID
			var messages []kernel.ID
			for range 4 {
				msg, err := newPosting(pool).PostToTopic(ctx, m, f.Channel.ID, &source, "choice")
				requireNoError(t, err)
				messages = append(messages, msg.ID)
			}
			conclusion := messages[2]
			requireNoError(t, conversationpg.NewResolution(pool, compose.PostingSequence, compose.PostingEvents, nil).Resolve(ctx, m, f.Channel.ID, source, conclusion))
			selected := []kernel.ID{messages[0], messages[1], messages[3]}
			wantConclusion, conclusionLocation := conclusion, source
			kinds := []realtime.EventKind{conversation.KindMessagesMoved, conversation.KindPosted}
			if tc.includesConclusion {
				selected[1] = conclusion
				wantConclusion, conclusionLocation = kernel.ID{}, destination
				kinds = append(kinds, conversation.KindResolutionChanged)
			}
			before := eventSeq(t, pool, f.OrganizationID)
			n := &recordingNotifier{t: t, pool: pool}
			_, err := conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, n).Branch(ctx, m, f.Channel.ID,
				conversation.Branch{Messages: selected, From: source, To: &destination}, func(conversation.Topic) string { return "moved" })
			requireNoError(t, err)
			assertResolutionCommit(t, ctx, pool, f.OrganizationID, f.Channel.ID, source, conclusion, wantConclusion, conclusionLocation, before, kinds)
			for _, id := range selected {
				var location kernel.ID
				requireNoError(t, pool.QueryRow(ctx, `SELECT topic_id FROM message WHERE organization_id = $1 AND id = $2`, f.OrganizationID, id).Scan(&location))
				if location != destination {
					t.Fatalf("selected message %s stayed in topic %s", realtime.FormatPayloadID(id), realtime.FormatPayloadID(location))
				}
			}
			var highest int64
			requireNoError(t, pool.QueryRow(ctx, `SELECT max(seq) FROM event_log WHERE organization_id = $1 AND seq > $2`, f.OrganizationID, before).Scan(&highest))
			if len(n.raised) != 1 {
				t.Fatalf("branch notifications = %d, want 1", len(n.raised))
			}
			got := n.raised[0]
			if got.organizationID != f.OrganizationID || got.seq != highest || got.committed != highest {
				t.Fatalf("branch notification: organisation=%s seq=%d committed=%d; want organisation=%s highest appended seq=%d", realtime.FormatPayloadID(got.organizationID), got.seq, got.committed, realtime.FormatPayloadID(f.OrganizationID), highest)
			}
		})
	}
}

func TestResolutionCrossOrganization(t *testing.T) {
	for _, operation := range []string{"resolve", "reopen resolved", "reopen open"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			pool := pgtest.New(t)
			ctx := t.Context()
			a := conversationtest.OrganizationWithOwner(t, pool, "resolution-a", "general")
			b := conversationtest.OrganizationWithOwner(t, pool, "resolution-b", "general")
			owner := membership(a.OrganizationID, a.MemberID)
			owner.Member.OrganizationID = a.OrganizationID
			foreign := membership(b.OrganizationID, b.MemberID)
			foreign.Member.OrganizationID = b.OrganizationID
			topic := conversationtest.Topic(t, pool, a.OrganizationID, a.Channel.ID, "decision").ID
			msg, err := newPosting(pool).PostToTopic(ctx, owner, a.Channel.ID, &topic, "conclusion")
			requireNoError(t, err)
			r := conversationpg.NewResolution(pool, compose.PostingSequence, compose.PostingEvents, nil)
			if operation != "reopen open" {
				requireNoError(t, r.Resolve(ctx, owner, a.Channel.ID, topic, msg.ID))
			}
			beforeA := readBranchState(t, pool, a.OrganizationID)
			beforeB := readBranchState(t, pool, b.OrganizationID)
			var beforeConclusion kernel.ID
			var beforeVersion string
			requireNoError(t, pool.QueryRow(ctx, `SELECT coalesce(conclusion_message_id, '00000000-0000-0000-0000-000000000000'), xmin::text FROM topic WHERE id = $1`, topic).Scan(&beforeConclusion, &beforeVersion))
			n := &recordingNotifier{t: t, pool: pool}
			r = conversationpg.NewResolution(pool, compose.PostingSequence, compose.PostingEvents, n)
			if operation == "resolve" {
				err = r.Resolve(ctx, foreign, a.Channel.ID, topic, msg.ID)
			} else {
				err = r.Reopen(ctx, foreign, a.Channel.ID, topic)
			}
			if !errors.Is(err, conversation.ErrTopicNotFound) {
				t.Errorf("%s across organisations = %v, want %v", operation, err, conversation.ErrTopicNotFound)
			}
			assertBranchState(t, readBranchState(t, pool, a.OrganizationID), beforeA)
			assertBranchState(t, readBranchState(t, pool, b.OrganizationID), beforeB)
			var conclusion kernel.ID
			var version string
			requireNoError(t, pool.QueryRow(ctx, `SELECT coalesce(conclusion_message_id, '00000000-0000-0000-0000-000000000000'), xmin::text FROM topic WHERE id = $1`, topic).Scan(&conclusion, &version))
			if conclusion != beforeConclusion || version != beforeVersion || len(n.raised) != 0 {
				t.Fatalf("%s across organisations changed conclusion/version or notified: conclusion=%s version=%s notifications=%d; want conclusion=%s version=%s notifications=0", operation, realtime.FormatPayloadID(conclusion), version, len(n.raised), realtime.FormatPayloadID(beforeConclusion), beforeVersion)
			}
		})
	}
}
