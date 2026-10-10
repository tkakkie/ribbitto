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

// ListedMember is the public identity of one member in a bounded listing.
type ListedMember struct {
	ID kernel.ID
	DirectoryEntry
}

// Directory reads public member identities within one organisation. ID lookups
// omit missing and foreign members. It exposes no membership credentials.
type Directory interface {
	// ListMembers returns at most limit members in ID order, after the optional ID.
	// Limits must be between 1 and 101, including a page's lookahead row.
	ListMembers(context.Context, kernel.ID, *kernel.ID, int32) ([]ListedMember, error)
	LookupMembers(context.Context, kernel.ID, []kernel.ID) (map[kernel.ID]DirectoryEntry, error)
}
