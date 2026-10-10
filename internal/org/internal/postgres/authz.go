package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
)

// AuthzStore implements org.MembershipStore.
type AuthzStore struct {
	queries *sqlcgen.Queries
}

// NewAuthzStore returns an AuthzStore that runs its queries on db.
func NewAuthzStore(db sqlcgen.DBTX) *AuthzStore {
	return &AuthzStore{queries: sqlcgen.New(db)}
}

// Membership returns the account's membership in the organisation with slug.
func (s *AuthzStore) Membership(ctx context.Context, accountID kernel.ID, slug string) (org.Membership, error) {
	row, err := s.queries.GetMembershipBySlug(ctx, sqlcgen.GetMembershipBySlugParams{
		Slug:      slug,
		AccountID: uuid(accountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return org.Membership{}, org.ErrNotFound
	}
	if err != nil {
		return org.Membership{}, fmt.Errorf("selecting membership: %w", err)
	}
	return org.Membership{
		AccessEpoch:  row.AccessEpoch,
		Organization: org.Organization{ID: row.OrganizationID.Bytes, Slug: row.Slug, Name: row.Name},
		Member: org.Member{
			ID:             row.MemberID.Bytes,
			OrganizationID: row.OrganizationID.Bytes,
			AccountID:      accountID,
			Role:           org.Role(row.Role),
			Handle:         row.Handle,
			JoinedEventSeq: row.JoinedEventSeq,
		},
	}, nil
}

// HomeSlug returns the setup organisation's slug if the account is a member.
func (s *AuthzStore) HomeSlug(ctx context.Context, accountID kernel.ID) (string, error) {
	slug, err := s.queries.GetHomeSlug(ctx, uuid(accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", org.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("selecting home organisation: %w", err)
	}
	return slug, nil
}

// AccessEpoch reads one organisation's epoch in a fresh pool statement.
func (s *AuthzStore) AccessEpoch(ctx context.Context, organizationID kernel.ID) (int64, error) {
	epoch, err := s.queries.GetAccessEpoch(ctx, uuid(organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, org.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("selecting access epoch: %w", err)
	}
	return epoch, nil
}
