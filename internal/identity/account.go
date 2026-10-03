package identity

import "github.com/tkakkie/ribbitto/internal/domain"

// Account is a person who can sign in. It is global, not tied to one
// organisation; its password hash never leaves the persistence layer.
type Account struct {
	ID          domain.ID
	Email       string
	DisplayName string
}
