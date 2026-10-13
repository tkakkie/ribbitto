package conversationpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// The use cases see only the member's organisation, even with a known id.
func TestChannels(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := orgtest.Organization(t, pool, "acme", "acme", 0)
	globex := orgtest.Organization(t, pool, "globex", "globex", 0)
	member := func(orgID kernel.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{OrganizationID: orgID}}
	}
	service := conversationpg.NewChannels(pool, conversation.DefaultMaxChannels)
	for _, org := range []kernel.ID{acme, globex} {
		conversationtest.Channel(t, pool, org, conversation.DefaultChannelName, true)
	}
	secret, err := service.Create(ctx, member(globex), " 開発 ")
	if err != nil || secret.Name != "開発" || secret.IsDefault {
		t.Fatalf("create: %+v, %v", secret, err)
	}
	if _, err := service.Create(ctx, member(globex), "開発"); !errors.Is(err, conversation.ErrChannelNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := service.Create(ctx, member(acme), "開発"); err != nil {
		t.Fatalf("same name in another organisation: %v", err)
	}
	if _, err := service.Get(ctx, member(acme), secret.ID); !errors.Is(err, conversation.ErrChannelNotFound) {
		t.Fatalf("another organisation's channel by id: %v", err)
	}
	if got, err := service.Default(ctx, member(globex)); err != nil || got.Name != conversation.DefaultChannelName || got.OrganizationID != globex {
		t.Fatalf("default: %+v, %v", got, err)
	}
}

func TestChannelLimitConcurrent(t *testing.T) {
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := orgtest.Organization(t, pool, "acme", "acme", 0)
	m := org.Membership{Organization: org.Organization{ID: id}}
	conversationtest.Channel(t, pool, id, conversation.DefaultChannelName, true)
	service := conversationpg.NewChannels(pool, 2)
	// Hold the creation lock until both callers are waiting, so this exercises
	// overlapping transactions rather than two accidentally sequential calls.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, "SELECT id FROM channel WHERE organization_id = $1 AND is_default FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() { _, err := service.Create(ctx, m, name); results <- err }()
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("creators did not both wait for the creation lock")
		case <-ticker.C:
		}
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	success, limited := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, conversation.ErrChannelLimit):
			limited++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || limited != 1 {
		t.Fatalf("concurrent creates = %d successes, %d limited; want 1 each", success, limited)
	}
	// Lowering the cap must refuse without deleting channels or their topics.
	for _, cap := range []int32{2, 1} {
		if _, err := conversationpg.NewChannels(pool, cap).Create(ctx, m, "blocked"); !errors.Is(err, conversation.ErrChannelLimit) {
			t.Fatalf("cap %d: %v", cap, err)
		}
	}
	var channels, topics int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM channel WHERE organization_id = $1), (SELECT count(*) FROM topic WHERE organization_id = $1)", id).Scan(&channels, &topics); err != nil {
		t.Fatal(err)
	}
	if channels != 2 || topics != 2 {
		t.Fatalf("rows = %d channels, %d topics; want 2 each", channels, topics)
	}
	other := orgtest.Organization(t, pool, "other", "other", 0)
	conversationtest.Channel(t, pool, other, conversation.DefaultChannelName, true)
	if _, err := service.Create(ctx, org.Membership{Organization: org.Organization{ID: other}}, "one"); err != nil {
		t.Fatal(err)
	}
}
