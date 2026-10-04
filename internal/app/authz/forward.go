package authz

import "github.com/tkakkie/ribbitto/internal/org"

// Membership is org.Membership.
type Membership = org.Membership

// Authorizer is org.Authorizer.
type Authorizer = org.Authorizer

// Store is org.MembershipStore.
type Store = org.MembershipStore

// ErrNotFound is org.ErrNotFound.
var ErrNotFound = org.ErrNotFound

// New is org.NewAuthorizer.
func New(store Store) *Authorizer { return org.NewAuthorizer(store) }
