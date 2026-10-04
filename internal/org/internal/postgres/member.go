package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// MemberStore implements org.HandleStore.
type MemberStore struct {
	queries *sqlcgen.Queries
}

// NewMemberStore returns a MemberStore that runs its queries on db.
func NewMemberStore(db sqlcgen.DBTX) *MemberStore {
	return &MemberStore{queries: sqlcgen.New(db)}
}

// UpdateHandle changes one member's handle. The unique constraint, not a
// prior lookup, decides between concurrent claims of the same handle.
func (s *MemberStore) UpdateHandle(ctx context.Context, organizationID, memberID kernel.ID, handle string) error {
	rows, err := s.queries.UpdateMemberHandle(ctx, sqlcgen.UpdateMemberHandleParams{
		OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true},
		ID:             pgtype.UUID{Bytes: memberID, Valid: true},
		Handle:         handle,
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_organization_id_handle_key":
		return org.ErrHandleTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "member_handle_"):
		return fmt.Errorf("%w: %w", org.ErrInvalidHandle, err)
	case err != nil:
		return fmt.Errorf("updating handle: %w", err)
	case rows == 0:
		return org.ErrNotFound
	}
	return nil
}

// Directory implements org.Directory inside a caller's snapshot.
type Directory struct {
	queries *sqlcgen.Queries
}

// NewDirectoryIn binds member lookups to the caller's snapshot, so authors
// reflect the same state as its messages and other directory reads.
func NewDirectoryIn(snapshot platform.Snapshot) *Directory {
	return &Directory{queries: sqlcgen.New(pgxbridge.Snapshot(snapshot))}
}

// LookupMembers implements org.Directory, scoped to one organisation.
func (s *Directory) LookupMembers(ctx context.Context, organizationID kernel.ID, ids []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	rows, err := s.queries.LookupMembers(ctx, sqlcgen.LookupMembersParams{OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, MemberIds: uuidArray(ids)})
	if err != nil {
		return nil, fmt.Errorf("looking up members: %w", err)
	}
	result := make(map[kernel.ID]org.DirectoryEntry, len(rows))
	for _, row := range rows {
		result[row.ID.Bytes] = org.DirectoryEntry{AccountID: row.AccountID.Bytes, Handle: row.Handle}
	}
	return result, nil
}

func uuidArray(ids []kernel.ID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	return result
}
