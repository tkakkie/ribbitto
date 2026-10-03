// Package identity is the identity module's root (decision 26): password
// hashing and server-side sessions, the credentials that let an account
// sign in.
//
// Module: identity (feature map in docs/architecture/features.md), which
// owns the account and session tables. Exported API: Hasher, Sessions and
// SignIn, Directory for batch display-name lookups, with the AccountStore and SessionStore interfaces they need,
// SessionLifetime and the errors they return. It writes no other feature's
// tables. It imports domain for Account and ValidateEmail until migration
// step 1a-2, and for the ID alias until step 5.
package identity
