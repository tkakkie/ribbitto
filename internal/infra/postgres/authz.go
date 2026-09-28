package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// AuthzStore implements authz.Store.
type AuthzStore struct {
	queries *sqlcgen.Queries
}

// NewAuthzStore returns an AuthzStore that runs its queries on db.
func NewAuthzStore(db sqlcgen.DBTX) *AuthzStore {
	return &AuthzStore{queries: sqlcgen.New(db)}
}

// Membership returns the account's membership in the organisation with slug.
func (s *AuthzStore) Membership(ctx context.Context, accountID domain.ID, slug string) (authz.Membership, error) {
	row, err := s.queries.GetMembershipBySlug(ctx, sqlcgen.GetMembershipBySlugParams{
		Slug:      slug,
		AccountID: pgtype.UUID{Bytes: accountID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.Membership{}, authz.ErrNotFound
	}
	if err != nil {
		return authz.Membership{}, fmt.Errorf("selecting membership: %w", err)
	}
	return authz.Membership{
		Organization: domain.Organization{ID: row.OrganizationID.Bytes, Slug: row.Slug, Name: row.Name},
		Member: domain.Member{
			ID:             row.MemberID.Bytes,
			OrganizationID: row.OrganizationID.Bytes,
			AccountID:      accountID,
			Role:           domain.Role(row.Role),
			Handle:         row.Handle,
		},
	}, nil
}

// HomeSlug returns the setup organisation's slug if the account is a member.
func (s *AuthzStore) HomeSlug(ctx context.Context, accountID domain.ID) (string, error) {
	slug, err := s.queries.GetHomeSlug(ctx, pgtype.UUID{Bytes: accountID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", authz.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("selecting home organisation: %w", err)
	}
	return slug, nil
}
