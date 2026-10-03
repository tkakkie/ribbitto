package identity

import "github.com/tkakkie/ribbitto/internal/kernel"

// Account is a person who can sign in. It is global, not tied to one
// organisation. It carries no credentials: the password hash goes only from
// identity's store to sign-in, through AccountStore.
type Account struct {
	ID          kernel.ID
	Email       string
	DisplayName string
}
