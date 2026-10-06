package org

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// DirectoryEntry is a member's public author information.
type DirectoryEntry struct {
	AccountID kernel.ID
	Handle    string
}

// Directory looks up only the requested member IDs within one organisation.
// Missing and foreign members are omitted. It exposes no membership credentials.
type Directory interface {
	LookupMembers(context.Context, kernel.ID, []kernel.ID) (map[kernel.ID]DirectoryEntry, error)
}
