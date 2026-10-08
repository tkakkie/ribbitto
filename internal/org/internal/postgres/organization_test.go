package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// Keep this membership-free lookup in test code so production queries cannot
// bypass the authorizer's membership check when resolving a slug.
func getOrganizationBySlug(ctx context.Context, pool *pgxpool.Pool, slug string) (sqlcgen.Organization, error) {
	var organization sqlcgen.Organization
	err := pool.QueryRow(ctx, "SELECT id, slug, name, event_seq, created_at, event_log_boundary_seq FROM organization WHERE slug = $1", slug).Scan(
		&organization.ID,
		&organization.Slug,
		&organization.Name,
		&organization.EventSeq,
		&organization.CreatedAt,
		&organization.EventLogBoundarySeq,
	)
	if err != nil {
		return sqlcgen.Organization{}, fmt.Errorf("looking up organization by slug: %w", err)
	}
	return organization, nil
}

func TestGetOrganizationBySlug(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	orgtest.Organization(t, pool, "example", "Example", 0)
	organization, err := getOrganizationBySlug(t.Context(), pool, "example")
	if err != nil {
		t.Fatal(err)
	}
	if organization.Slug != "example" || organization.Name != "Example" || !organization.ID.Valid || !organization.CreatedAt.Valid || organization.EventSeq != 0 {
		t.Fatalf("unexpected organization: %+v", organization)
	}
	if _, err := getOrganizationBySlug(t.Context(), pool, "unknown"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown slug: want pgx.ErrNoRows, got %v", err)
	}
}
