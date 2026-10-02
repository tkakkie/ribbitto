package postgres_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

// The topic table enforces decision 21's invariants on its own: one default
// per channel, unnamed exactly when default, names unique per channel
// ignoring case, and a channel of the same organisation.
func TestTopicSchema(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	store := postgres.NewTopicStore(pool)
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	globex := pgtest.OrganizationWithOwner(t, pool, "globex", "general")
	random := pgtest.Channel(t, pool, acme.OrganizationID, "random", false)
	general := acme.Channel.ID

	def, err := store.CreateDefaultTopic(ctx, acme.OrganizationID, general)
	if err != nil || !def.IsDefault || def.Name != "" || def.ChannelID != general || def.OrganizationID != acme.OrganizationID || def.ID[6]>>4 != 7 || def.CreatedAt.IsZero() {
		t.Fatalf("default topic: %+v, %v", def, err)
	}
	if _, err := store.CreateDefaultTopic(ctx, acme.OrganizationID, general); !isConstraint(err, "23505", "topic_default_idx") {
		t.Fatalf("second default topic: %v", err)
	}
	if _, err := store.CreateDefaultTopic(ctx, acme.OrganizationID, random.ID); err != nil {
		t.Fatalf("default topic of another channel: %v", err)
	}

	for _, input := range []string{"a", "　 設計 会議　 ", " é ", strings.Repeat("界", 80)} {
		name, err := domain.ValidateTopicName(input)
		requireNoError(t, err)
		created, err := store.CreateTopic(ctx, acme.OrganizationID, general, name)
		requireNoError(t, err)
		got, err := store.GetTopic(ctx, acme.OrganizationID, general, created.ID)
		if err != nil || got != created || got.Name != name || got.IsDefault {
			t.Fatalf("topic round trip: %+v, %v", got, err)
		}
	}
	if _, err := store.CreateTopic(ctx, acme.OrganizationID, general, "A"); !errors.Is(err, topic.ErrNameTaken) {
		t.Fatalf("duplicate name ignoring case: %v", err)
	}
	if _, err := store.CreateTopic(ctx, acme.OrganizationID, random.ID, "a"); err != nil {
		t.Fatalf("same name in another channel: %v", err)
	}
	for _, name := range []string{"", strings.Repeat("界", 81), "é"} {
		if _, err := store.CreateTopic(ctx, acme.OrganizationID, general, name); !errors.Is(err, topic.ErrInvalidName) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
	// Raw SQL writes rows the store cannot express: a named default and an
	// unnamed ordinary topic.
	_, err = pool.Exec(ctx, "INSERT INTO topic (organization_id, channel_id, name, is_default) VALUES ($1, $2, NULL, true)", acme.OrganizationID, globex.Channel.ID)
	if !isConstraint(err, "23503", "") {
		t.Fatalf("topic in another organisation's channel: %v", err)
	}
	for _, row := range []struct {
		name      any
		isDefault bool
	}{{"named default", true}, {nil, false}} {
		_, err := pool.Exec(ctx, "INSERT INTO topic (organization_id, channel_id, name, is_default) VALUES ($1, $2, $3, $4)", globex.OrganizationID, globex.Channel.ID, row.name, row.isDefault)
		if !isConstraint(err, "23514", "topic_default_unnamed_check") {
			t.Fatalf("name %v with is_default %v: %v", row.name, row.isDefault, err)
		}
	}
	// #307's composite foreign keys need these unique keys to exist.
	var keys []string
	rows, err := pool.Query(ctx, "SELECT conname FROM pg_constraint WHERE conrelid = 'topic'::regclass AND contype = 'u' ORDER BY conname")
	requireNoError(t, err)
	for rows.Next() {
		var name string
		requireNoError(t, rows.Scan(&name))
		keys = append(keys, name)
	}
	requireNoError(t, rows.Err())
	if want := []string{"topic_channel_id_default_key", "topic_channel_id_key"}; !slices.Equal(keys, want) {
		t.Fatalf("unique keys %v, want %v", keys, want)
	}
}

// Lookups and lists see only the given organisation and channel, even with
// a known topic id.
func TestTopicStoreScope(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	store := postgres.NewTopicStore(pool)
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	globex := pgtest.OrganizationWithOwner(t, pool, "globex", "general")
	random := pgtest.Channel(t, pool, acme.OrganizationID, "random", false)

	def, err := store.CreateDefaultTopic(ctx, acme.OrganizationID, acme.Channel.ID)
	requireNoError(t, err)
	var named []domain.Topic
	for _, name := range []string{"design", "release", "hiring"} {
		created, err := store.CreateTopic(ctx, acme.OrganizationID, acme.Channel.ID, name)
		requireNoError(t, err)
		named = append(named, created)
	}
	secret, err := store.CreateTopic(ctx, globex.OrganizationID, globex.Channel.ID, "secret")
	requireNoError(t, err)

	for _, lookup := range []struct {
		org, channel, id domain.ID
		what             string
	}{
		{acme.OrganizationID, acme.Channel.ID, secret.ID, "another organisation's topic"},
		{acme.OrganizationID, random.ID, def.ID, "another channel's topic"},
		{globex.OrganizationID, acme.Channel.ID, def.ID, "a channel of another organisation"},
	} {
		if _, err := store.GetTopic(ctx, lookup.org, lookup.channel, lookup.id); !errors.Is(err, topic.ErrNotFound) {
			t.Fatalf("%s: %v", lookup.what, err)
		}
	}

	all, err := store.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 10)
	if want := append([]domain.Topic{def}, named...); err != nil || !slices.Equal(all, want) {
		t.Fatalf("list: %+v, %v; want %+v", all, err, want)
	}
	if first, err := store.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 2); err != nil || !slices.Equal(first, all[:2]) {
		t.Fatalf("bounded list: %+v, %v", first, err)
	}
	if none, err := store.ListTopics(ctx, acme.OrganizationID, random.ID, 10); err != nil || len(none) != 0 {
		t.Fatalf("another channel's list: %+v, %v", none, err)
	}
	if leaked, err := store.ListTopics(ctx, globex.OrganizationID, acme.Channel.ID, 10); err != nil || len(leaked) != 0 {
		t.Fatalf("list across organisations: %+v, %v", leaked, err)
	}
	if _, err := store.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 0); err == nil {
		t.Fatal("unbounded list accepted")
	}
}

// isConstraint reports whether err is a PostgreSQL error with code and, when
// constraint is not empty, that constraint name.
func isConstraint(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && (constraint == "" || pgErr.ConstraintName == constraint)
}
