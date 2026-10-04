package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
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

	// Every channel is created with its default topic (#307).
	def, err := store.GetDefaultTopic(ctx, acme.OrganizationID, general)
	if err != nil || def.ID != acme.Channel.DefaultTopicID || !def.IsDefault || def.Name != "" || def.ChannelID != general || def.OrganizationID != acme.OrganizationID || def.ID[6]>>4 != 7 || def.CreatedAt.IsZero() {
		t.Fatalf("default topic: %+v, %v", def, err)
	}
	if _, err := store.CreateDefaultTopic(ctx, acme.OrganizationID, general); !isConstraint(err, "23505", "topic_default_idx") {
		t.Fatalf("second default topic: %v", err)
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

	def, err := store.GetDefaultTopic(ctx, acme.OrganizationID, acme.Channel.ID)
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
		if got, err := store.LookupTopics(ctx, lookup.org, lookup.channel, []domain.ID{lookup.id}); err != nil || len(got) != 0 {
			t.Fatalf("batch leaked %s: %+v, %v", lookup.what, got, err)
		}
		if _, err := store.GetTopic(ctx, lookup.org, lookup.channel, lookup.id); !errors.Is(err, topic.ErrNotFound) {
			t.Fatalf("%s: %v", lookup.what, err)
		}
	}

	slices.SortFunc(named, func(a, b domain.Topic) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	all, err := store.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 10)
	if want := append([]domain.Topic{def}, named...); err != nil || !slices.Equal(all, want) {
		t.Fatalf("list: %+v, %v; want %+v", all, err, want)
	}
	if first, err := store.ListTopics(ctx, acme.OrganizationID, acme.Channel.ID, 2); err != nil || !slices.Equal(first, all[:2]) {
		t.Fatalf("bounded list: %+v, %v", first, err)
	}
	if other, err := store.ListTopics(ctx, acme.OrganizationID, random.ID, 10); err != nil || len(other) != 1 || other[0].ID != random.DefaultTopicID {
		t.Fatalf("another channel's list: %+v, %v", other, err)
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

// Channels and messages can only point at topics of their own channel, and a
// channel only at its own default topic.
func TestTopicReferences(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	topics := postgres.NewTopicStore(pool)
	acme := pgtest.OrganizationWithOwner(t, pool, "acme", "general")
	random := pgtest.Channel(t, pool, acme.OrganizationID, "random", false)

	// Creating a channel creates its default topic, and posting without a
	// topic goes there.
	for _, c := range []domain.Channel{acme.Channel, random} {
		got, err := topics.GetDefaultTopic(ctx, acme.OrganizationID, c.ID)
		if err != nil || got.ID != c.DefaultTopicID || !got.IsDefault || got.ChannelID != c.ID {
			t.Fatalf("default topic of %s: %+v, %v", c.Name, got, err)
		}
	}
	posted, err := postgres.NewPostingStore(pool, appendEvents).Post(ctx, acme.OrganizationID, random.ID, acme.MemberID, "hello")
	if err != nil || posted.TopicID != random.DefaultTopicID {
		t.Fatalf("post: %+v, %v", posted, err)
	}
	named, err := topics.CreateTopic(ctx, acme.OrganizationID, acme.Channel.ID, "design")
	requireNoError(t, err)

	// Raw SQL writes the references the stores never write.
	for _, tc := range []struct{ name, sql, code, constraint string }{
		{"message in another channel's topic", "UPDATE message SET topic_id = $2 WHERE id = $1", "23503", "message_topic_fkey"},
		{"default flag cleared", "UPDATE channel SET default_topic_is_default = false WHERE id = $3", "23514", "channel_default_topic_is_default_check"},
		{"default topic deleted", "DELETE FROM topic WHERE id = $4", "23001", "channel_default_topic_fkey"},
	} {
		_, err := pool.Exec(ctx, "WITH fixture AS (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid) "+tc.sql, posted.ID, acme.Channel.DefaultTopicID, random.ID, random.DefaultTopicID)
		if !isConstraint(err, tc.code, tc.constraint) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	// The channel's key is deferred, so these fail at commit.
	for _, tc := range []struct{ name, sql string }{
		{"default pointing at a named topic", "UPDATE channel SET default_topic_id = $1 WHERE id = $2"},
		{"channel without its default topic", "INSERT INTO channel (organization_id, name) SELECT organization_id, 'orphan' FROM channel WHERE id = $2 AND $1::uuid IS NOT NULL"},
	} {
		tx, err := pool.Begin(ctx)
		requireNoError(t, err)
		_, err = tx.Exec(ctx, tc.sql, named.ID, acme.Channel.ID)
		requireNoError(t, err)
		if err := tx.Commit(ctx); !isConstraint(err, "23503", "channel_default_topic_fkey") {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

// Migration 9 gives every existing channel a default topic and moves every
// existing message into its channel's.
func TestTopicBackfill(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	requireNoError(t, err)
	_, err = provider.UpTo(ctx, 8)
	requireNoError(t, err)
	// Raw SQL writes what the binary of migration 8 wrote.
	for _, slug := range []string{"acme", "globex"} {
		orgID := pgtest.Organization(t, pool, slug, slug, 0)
		member := pgtest.Member(t, pool, orgID, pgtest.Account(t, pool, slug+"@example.org", slug), org.RoleOwner, "owner", 1)
		_, err := pool.Exec(ctx, `
			WITH c AS (INSERT INTO channel (organization_id, name, is_default) VALUES ($1, 'general', true), ($1, 'random', false), ($1, 'empty', false) RETURNING id, name)
			INSERT INTO message (organization_id, channel_id, member_id, body, event_seq)
			SELECT $1, c.id, $2, 'm' || n, n + CASE c.name WHEN 'general' THEN 0 ELSE 10 END
			FROM c, generate_series(1, 3) n WHERE c.name <> 'empty'`, orgID, member)
		requireNoError(t, err)
	}
	_, err = provider.UpTo(ctx, 9)
	requireNoError(t, err)
	var channels, topicsFound, wrong, messages, misplaced int
	requireNoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM channel),
		(SELECT count(*) FROM topic WHERE is_default AND name IS NULL),
		(SELECT count(*) FROM channel c LEFT JOIN topic t ON t.id = c.default_topic_id AND t.organization_id = c.organization_id AND t.channel_id = c.id AND t.is_default WHERE t.id IS NULL),
		(SELECT count(*) FROM message),
		(SELECT count(*) FROM message m JOIN channel c ON c.organization_id = m.organization_id AND c.id = m.channel_id WHERE m.topic_id <> c.default_topic_id)`).
		Scan(&channels, &topicsFound, &wrong, &messages, &misplaced))
	if channels != 6 || topicsFound != 6 || wrong != 0 || messages != 12 || misplaced != 0 {
		t.Fatalf("channels=%d default topics=%d without one=%d messages=%d misplaced=%d", channels, topicsFound, wrong, messages, misplaced)
	}
	// Down keeps named topics and removes only the defaults; up again
	// leaves the same shape, without a second default.
	_, err = pool.Exec(ctx, "INSERT INTO topic (organization_id, channel_id, name) SELECT organization_id, id, 'design' FROM channel WHERE name = 'general'")
	requireNoError(t, err)
	_, err = provider.Down(ctx)
	requireNoError(t, err)
	var named, defaults int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE name = 'design'), count(*) FILTER (WHERE is_default) FROM topic").Scan(&named, &defaults))
	if named != 2 || defaults != 0 {
		t.Fatalf("after down: %d named topics, %d defaults; want 2 and 0", named, defaults)
	}
	_, err = provider.UpTo(ctx, 9)
	requireNoError(t, err)
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE name = 'design'), count(*) FILTER (WHERE is_default) FROM topic").Scan(&named, &defaults))
	if named != 2 || defaults != 6 {
		t.Fatalf("after down and up: %d named topics, %d defaults; want 2 and 6", named, defaults)
	}
}
