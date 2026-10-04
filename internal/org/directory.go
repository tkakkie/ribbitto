package org

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// DirectoryEntry is a member's public author information.
type DirectoryEntry struct {
	AccountID domain.ID
	Handle    string
}

// Directory looks up only the requested member IDs within one organisation.
// Missing and foreign members are omitted. It exposes no membership credentials.
type Directory interface {
	LookupMembers(context.Context, domain.ID, []domain.ID) (map[domain.ID]DirectoryEntry, error)
}
