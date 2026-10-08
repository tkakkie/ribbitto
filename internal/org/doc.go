// Package org is the org module's root (decision 26) and the single place
// that decides who may see an organisation's data. Handlers and the
// real-time delivery loop call it; they never decide access themselves.
//
// Module: org (feature map in docs/architecture/features.md), which owns the
// organization, member and setup tables and publishes member.joined.
//
// A handle change resolves the caller's member from the session and URL's
// organisation, never from a request. Setup authorizes and validates before
// hashing; setup and sign-up own their transactions so the account, member
// and sequence commit together. Setup also creates the default channel in
// that transaction. Cross-feature writes use injected writers.
package org
