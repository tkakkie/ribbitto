package identity

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// Directory exposes identity's display names for only the requested accounts.
// Missing accounts are omitted; emails and credentials are never returned.
type Directory interface {
	LookupDisplayNames(context.Context, []domain.ID) (map[domain.ID]string, error)
}
