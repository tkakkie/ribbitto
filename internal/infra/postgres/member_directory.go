package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// MemberDirectory is the author lookup MessageReader needs from org.
// The consumer moves to conversation with the page snapshot in step 4.
type MemberDirectory interface {
	LookupMembers(context.Context, domain.ID, []domain.ID) (map[domain.ID]org.DirectoryEntry, error)
}

// MemberDirectoryIn binds a member directory to the reader's snapshot.
type MemberDirectoryIn func(platform.Snapshot) MemberDirectory
