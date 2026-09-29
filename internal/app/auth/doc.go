// Package auth implements password hashing and server-side sessions: the
// credentials that let an account sign in.
//
// Feature: identity (feature map in docs/architecture/README.md), which
// owns the account and session tables. Exported API: Hasher, Sessions and
// SignIn, Directory for batch display-name lookups, with the AccountStore and SessionStore interfaces they need,
// SessionLifetime and the errors they return. It writes no other feature's
// tables.
package auth
