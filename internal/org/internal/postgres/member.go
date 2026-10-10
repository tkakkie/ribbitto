package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
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
		OrganizationID: uuid(organizationID),
		ID:             uuid(memberID),
		Handle:         handle,
	})
	if err != nil {
		if mapped := memberHandleError(err); mapped != nil {
			return mapped
		}
		return fmt.Errorf("updating handle: %w", err)
	}
	if rows == 0 {
		return org.ErrNotFound
	}
	return nil
}

func memberHandleError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "member_organization_id_handle_key":
		return org.ErrHandleTaken
	case pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "member_handle_"):
		return fmt.Errorf("%w: %w", org.ErrInvalidHandle, err)
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
	rows, err := s.queries.LookupMembers(ctx, sqlcgen.LookupMembersParams{OrganizationID: uuid(organizationID), MemberIds: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("looking up members: %w", err)
	}
	result := make(map[kernel.ID]org.DirectoryEntry, len(rows))
	for _, row := range rows {
		result[row.ID.Bytes] = org.DirectoryEntry{AccountID: row.AccountID.Bytes, Handle: row.Handle}
	}
	return result, nil
}

// ListMembers reads a keyset page of public member identities in one organisation.
func (s *Directory) ListMembers(ctx context.Context, organizationID kernel.ID, after *kernel.ID, limit int32) ([]org.ListedMember, error) {
	if limit < 1 || limit > 101 {
		return nil, fmt.Errorf("member page limit must be between 1 and 101")
	}
	params := sqlcgen.ListMembersParams{OrganizationID: uuid(organizationID), PageLimit: limit}
	if after != nil {
		params.AfterID = uuid(*after)
	}
	rows, err := s.queries.ListMembers(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	result := make([]org.ListedMember, len(rows))
	for i, row := range rows {
		result[i] = org.ListedMember{ID: row.ID.Bytes, DirectoryEntry: org.DirectoryEntry{AccountID: row.AccountID.Bytes, Handle: row.Handle}}
	}
	return result, nil
}
