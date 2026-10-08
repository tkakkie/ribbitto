// Package identity is the identity module's root (decision 26): password
// hashing and server-side sessions, the credentials that let an account
// sign in.
//
// Module: identity (feature map in docs/architecture/features.md), which
// owns the account and session tables and the email, password and display-name
// rules. It writes no other feature's tables and publishes no event kinds.
package identity
