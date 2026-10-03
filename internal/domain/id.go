package domain

import "github.com/tkakkie/ribbitto/internal/kernel"

// ID identifies a stored entity. It is kernel.ID, kept here as an alias so
// that existing code is unchanged until the migration's last step removes
// this package (decision 26).
type ID = kernel.ID
