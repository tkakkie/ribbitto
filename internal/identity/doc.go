// Package identity is the identity module's root (decision 26): password
// hashing and server-side sessions, the credentials that let an account
// sign in.
//
// Module: identity (feature map in docs/architecture/features.md), which
// owns the account and session tables. Exported API: Hasher, Sessions and
// SignIn, Directory for batch display-name lookups, with the AccountStore and
// SessionStore interfaces they need, SessionLifetime and their errors. Account
// creation errors are ErrEmailTaken and ErrInvalidEmail. It writes no other
// feature's tables. It owns the email and password rules (ValidateEmail,
// ValidatePassword) and Account, and uses kernel.ID for identifiers.
package identity
