// Package identity is the identity module's root (decision 26): password
// hashing and server-side sessions, the credentials that let an account
// sign in. Password validation and sign-in share NFC normalisation; sign-in
// migrates legacy hashes without overwriting concurrent credential changes.
//
// Module: identity (feature map in docs/architecture/features.md), which
// owns the account and session tables, the email, password and display-name
// rules, and the account's send-key preference. It writes no other feature's
// tables and publishes no event kinds.
package identity
