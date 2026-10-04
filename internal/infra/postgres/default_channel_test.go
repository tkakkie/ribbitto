package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
	appchannel "github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

// completeSetup runs org's setup with infra's default-channel creator.
func completeSetup(t *testing.T, pool *pgxpool.Pool) (org.SetupResult, error) {
	t.Helper()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	return orgpg.NewSetup(pool, hasher, "secret",
		func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) },
		func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) },
		func(tx platform.Tx) org.DefaultChannelCreator { return postgres.DefaultChannelCreatorIn(tx) },
	).Complete(t.Context(), "secret", org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
}

// Organisations from before default channels get exactly one each.
func TestDefaultChannelBackfill(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool := pgtest.NewEmpty(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// acme: what the old setup left (an organisation and its setup row, no
	// channel); globex: a fixture without setup (setup is one-time);
	// initech: a non-default "general" already exists; hooli: already has a
	// default under another name.
	// Raw SQL preserves pre-upgrade states that current-schema fixtures cannot express.
	_, err = pool.Exec(ctx, `
		INSERT INTO organization (slug, name) VALUES ('acme', 'Acme'), ('globex', 'Globex'), ('initech', 'Initech'), ('hooli', 'Hooli');
		INSERT INTO setup (organization_id) SELECT id FROM organization WHERE slug = 'acme';
		INSERT INTO channel (organization_id, name, is_default)
		SELECT o.id, c.name, c.is_default FROM organization o, (VALUES ('general', false), ('random', false)) AS c(name, is_default) WHERE o.slug = 'initech'
		UNION ALL SELECT id, '雑談', true FROM organization WHERE slug = 'hooli'`)
	if err != nil {
		t.Fatal(err)
	}
	var initechGeneral, hooliDefault string
	if err := pool.QueryRow(ctx, "SELECT (SELECT c.id::text FROM channel c JOIN organization o ON o.id = c.organization_id WHERE o.slug = 'initech' AND c.name = 'general'), (SELECT c.id::text FROM channel c JOIN organization o ON o.id = c.organization_id WHERE o.slug = 'hooli')").Scan(&initechGeneral, &hooliDefault); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `
		SELECT o.slug, count(*) FILTER (WHERE c.is_default), min(c.id::text) FILTER (WHERE c.is_default), min(c.name) FILTER (WHERE c.is_default), count(*)
		FROM organization o LEFT JOIN channel c ON c.organization_id = o.id GROUP BY o.slug`)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		defaults, channels int
		id, name           string
	}
	got := map[string]result{}
	for rows.Next() {
		var slug string
		var r result
		if err := rows.Scan(&slug, &r.defaults, &r.id, &r.name, &r.channels); err != nil {
			t.Fatal(err)
		}
		got[slug] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]result{
		"acme":    {defaults: 1, channels: 1, name: "general"},
		"globex":  {defaults: 1, channels: 1, name: "general"},
		"initech": {defaults: 1, channels: 2, name: "general", id: initechGeneral},
		"hooli":   {defaults: 1, channels: 1, name: "雑談", id: hooliDefault},
	} {
		r := got[slug]
		if r.defaults != want.defaults || r.channels != want.channels || r.name != want.name || want.id != "" && r.id != want.id {
			t.Errorf("%s: got %+v, want %+v", slug, r, want)
		}
	}
}

// The use cases see only the member's organisation, even with a known id.
func TestChannelService(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := pgtest.Organization(t, pool, "acme", "Acme", 0)
	globex := pgtest.Organization(t, pool, "globex", "Globex", 0)
	member := func(orgID domain.ID) org.Membership {
		return org.Membership{Organization: org.Organization{ID: orgID}, Member: org.Member{OrganizationID: orgID}}
	}
	store := postgres.NewChannelStore(pool)
	service := appchannel.New(store)
	for _, org := range []domain.ID{acme, globex} {
		pgtest.Channel(t, pool, org, appchannel.DefaultName, true)
	}
	secret, err := service.Create(ctx, member(globex), " 開発 ")
	if err != nil || secret.Name != "開発" || secret.IsDefault {
		t.Fatalf("create: %+v, %v", secret, err)
	}
	if _, err := service.Create(ctx, member(globex), "開発"); !errors.Is(err, appchannel.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := service.Create(ctx, member(acme), "開発"); err != nil {
		t.Fatalf("same name in another organisation: %v", err)
	}
	if _, err := service.Get(ctx, member(acme), secret.ID); !errors.Is(err, appchannel.ErrNotFound) {
		t.Fatalf("another organisation's channel by id: %v", err)
	}
	list, err := service.List(ctx, member(acme))
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	for _, c := range list {
		if c.OrganizationID != acme || c.ID == secret.ID {
			t.Fatalf("list leaked %+v", c)
		}
	}
	if got, err := service.Default(ctx, member(globex)); err != nil || got.Name != appchannel.DefaultName || got.OrganizationID != globex {
		t.Fatalf("default: %+v, %v", got, err)
	}
}
