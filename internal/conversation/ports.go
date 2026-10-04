package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// MemberDirectory is the author lookup the page reader needs from org. It
// returns only the requested members of one organisation, omitting missing
// and foreign ones, as org's result type (no copy of it here).
type MemberDirectory interface {
	LookupMembers(ctx context.Context, organizationID kernel.ID, memberIDs []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error)
}

// MemberDirectoryIn binds a member directory to the caller's snapshot, so the
// authors are read in the same snapshot as the messages they wrote.
type MemberDirectoryIn func(platform.Snapshot) MemberDirectory

// AccountDirectory is the display-name lookup the page reader needs from
// identity. It returns only the requested accounts, omitting missing ones.
type AccountDirectory interface {
	LookupDisplayNames(ctx context.Context, accountIDs []kernel.ID) (map[kernel.ID]string, error)
}

// AccountDirectoryIn binds an account directory to the caller's snapshot, the
// same one the member directory and the messages are read in.
type AccountDirectoryIn func(platform.Snapshot) AccountDirectory
