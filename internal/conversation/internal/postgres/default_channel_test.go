package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// The creator writes on the caller's transaction: its channel and default
// topic are visible there and gone once the caller rolls back.
func TestDefaultChannelCreatorIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := fixtureOrganization(t, pool, "acme")
	var createDefault org.DefaultChannelCreatorIn = func(tx platform.Tx) org.DefaultChannelCreator { return conversationpg.DefaultChannelCreatorIn(tx) }
	rollback := errors.New("caller rolls back")
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
		requireNoError(t, createDefault(tx).CreateDefaultChannel(ctx, acme))
		pgxTx := pgxbridge.Tx(tx)
		got, err := postgres.NewChannelStore(pgxTx).GetDefaultChannel(ctx, acme)
		requireNoError(t, err)
		if got.Name != conversation.DefaultChannelName || !got.IsDefault || got.OrganizationID != acme {
			t.Fatalf("default channel in caller's transaction: %+v", got)
		}
		// Raw SQL, the lookup infra's GetDefaultTopic ran: the store reads no default topics.
		var topic kernel.ID
		requireNoError(t, pgxTx.QueryRow(ctx, "SELECT id FROM topic WHERE organization_id = $1 AND channel_id = $2 AND is_default", acme, got.ID).Scan(&topic))
		if topic != got.DefaultTopicID {
			t.Fatalf("default topic %v, want %v", topic, got.DefaultTopicID)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("caller rollback: %v", err)
	}
	var rows int
	requireNoError(t, pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM channel) + (SELECT count(*) FROM topic)").Scan(&rows))
	if rows != 0 {
		t.Fatalf("%d channel and topic rows remain after rollback", rows)
	}
}
