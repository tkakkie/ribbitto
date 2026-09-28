package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// MemberStore implements member.Store.
type MemberStore struct {
	queries *sqlcgen.Queries
}

// NewMemberStore returns a MemberStore that runs its queries on db.
func NewMemberStore(db sqlcgen.DBTX) *MemberStore {
	return &MemberStore{queries: sqlcgen.New(db)}
}

// UpdateHandle changes one member's handle. The unique constraint, not a
// prior lookup, decides between concurrent claims of the same handle.
func (s *MemberStore) UpdateHandle(ctx context.Context, organizationID, memberID domain.ID, handle string) error {
	rows, err := s.queries.UpdateMemberHandle(ctx, sqlcgen.UpdateMemberHandleParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ID:             pgtype.UUID{Bytes: memberID, Valid: true},
		Handle:         handle,
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_organization_id_handle_key":
		return member.ErrHandleTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "member_handle_"):
		return fmt.Errorf("%w: %w", member.ErrInvalidHandle, err)
	case err != nil:
		return fmt.Errorf("updating handle: %w", err)
	case rows == 0:
		return authz.ErrNotFound
	}
	return nil
}
