package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// The creator writes on the caller's transaction: its channel and default
// topic are visible there and gone once the caller rolls back.
func TestDefaultChannelCreatorIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := pgtest.Organization(t, pool, "acme", "Acme", 0)
	var createDefault org.DefaultChannelCreatorIn = func(tx platform.Tx) org.DefaultChannelCreator { return postgres.DefaultChannelCreatorIn(tx) }
	rollback := errors.New("caller rolls back")
	err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
		requireNoError(t, createDefault(tx).CreateDefaultChannel(ctx, acme))
		pgxTx := pgxbridge.Tx(tx)
		got, err := postgres.NewChannelStore(pgxTx).GetDefaultChannel(ctx, acme)
		requireNoError(t, err)
		if got.Name != conversation.DefaultChannelName || !got.IsDefault || got.OrganizationID != acme {
			t.Fatalf("default channel in caller's transaction: %+v", got)
		}
		topic, err := postgres.NewTopicStore(pgxTx).GetDefaultTopic(ctx, acme, got.ID)
		requireNoError(t, err)
		if topic.ID != got.DefaultTopicID {
			t.Fatalf("default topic %v, want %v", topic.ID, got.DefaultTopicID)
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
