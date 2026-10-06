package conversationpg_test

import (
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestPostedEventTopic(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "posted-topic", "general")
	// Raw SQL: no public use case creates a named topic without moving messages.
	var named kernel.ID
	requireNoError(t, pool.QueryRow(ctx, "INSERT INTO topic (organization_id, channel_id, name) VALUES ($1, $2, 'design') RETURNING id", f.OrganizationID, f.Channel.ID).Scan(&named))
	posting, m := newPosting(pool), membership(f.OrganizationID, f.MemberID)
	posted, err := posting.PostToTopic(ctx, m, f.Channel.ID, &named, "hello")
	requireNoError(t, err)
	reader := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds())
	events, err := reader.EventsAfter(ctx, f.OrganizationID, posted.EventSeq-1, 1)
	requireNoError(t, err)
	if len(events) != 1 || !slices.Equal(events[0].Topics, []kernel.ID{named}) {
		t.Fatalf("named-topic post: %+v", events)
	}
	// An absent topic_id is readable legacy data (conversation.DecodePosted's
	// tests cover the malformed values).
	_, err = posting.Post(ctx, m, f.Channel.ID, "second")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE event_log SET data = data - 'topic_id' WHERE organization_id = $1 AND seq = $2", f.OrganizationID, posted.EventSeq+1)
	requireNoError(t, err)
	events, err = reader.EventsAfter(ctx, f.OrganizationID, posted.EventSeq-1, 2)
	requireNoError(t, err)
	if len(events) != 2 || events[1].Topics != nil {
		t.Fatalf("legacy post: %+v", events)
	}
}
