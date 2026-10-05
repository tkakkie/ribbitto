package conversationpg_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// eventKinds gives readers the same publisher registrations as cmd/ribbitto.
func eventKinds() realtime.Kinds {
	kinds := conversationpg.EventKinds()
	for kind, router := range orgpg.EventKinds() {
		kinds[kind] = router
	}
	return kinds
}

// membership is the posting member's membership; posting takes nothing else.
func membership(organizationID, memberID kernel.ID) org.Membership {
	return org.Membership{Organization: org.Organization{ID: organizationID}, Member: org.Member{ID: memberID}}
}

// newPosting builds posting on org's sequence and realtime's appender, as
// cmd/ribbitto does, without notifications.
func newPosting(pool *pgxpool.Pool) *conversation.Posting {
	return conversationpg.NewPosting(pool,
		func(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) },
		func(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }, nil)
}
