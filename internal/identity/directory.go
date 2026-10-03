package identity

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Directory exposes identity's display names for only the requested accounts.
// Missing accounts are omitted; emails and credentials are never returned.
type Directory interface {
	LookupDisplayNames(context.Context, []kernel.ID) (map[kernel.ID]string, error)
}
