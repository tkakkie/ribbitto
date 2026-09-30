package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

func TestGetOrganizationBySlug(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	pgtest.Organization(t, pool, "example", "Example", 0)
	queries := sqlcgen.New(pool)
	organization, err := queries.GetOrganizationBySlug(t.Context(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if organization.Slug != "example" || organization.Name != "Example" || !organization.ID.Valid || !organization.CreatedAt.Valid || organization.EventSeq != 0 {
		t.Fatalf("unexpected organization: %+v", organization)
	}
	if _, err := queries.GetOrganizationBySlug(t.Context(), "unknown"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown slug: want pgx.ErrNoRows, got %v", err)
	}
}
