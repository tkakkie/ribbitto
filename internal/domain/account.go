package domain

import "github.com/tkakkie/ribbitto/internal/kernel"

// ID identifies a stored entity. It is kernel.ID, kept here as an alias so
// that existing code is unchanged until the migration's last step removes
// this package (decision 26).
type ID = kernel.ID

// Account is a person who can sign in. It is global, not tied to one
// organisation; its password hash never leaves the persistence layer.
type Account struct {
	ID          ID
	Email       string
	DisplayName string
}
