package identity

import (
	"errors"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ErrEmailTaken means an account already has this email address.
var ErrEmailTaken = errors.New("email is already taken")

// ErrInvalidEmail means the email violates an account email constraint.
var ErrInvalidEmail = errors.New("email is invalid")

// Account is a person who can sign in. It is global, not tied to one
// organisation. It carries no credentials: the password hash goes only from
// identity's store to sign-in, through AccountStore.
type Account struct {
	ID          kernel.ID
	Email       string
	DisplayName string
}
