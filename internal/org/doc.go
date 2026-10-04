// Package org is the org module's root (decision 26) and the single place
// that decides who may see an organisation's data. Handlers and the
// real-time delivery loop call it; they never decide access themselves.
//
// Feature: org (feature map in docs/architecture/features.md), which owns the
// organization, member and setup tables; this package only reads them.
// Exported API: Authorizer, built by NewAuthorizer (including MayReceive,
// which the real-time stream calls for each event after rendering it, right
// before sending), Membership, the MembershipStore interface it needs and
// ErrNotFound. Every feature checks access through it. Until org's store
// moves (step 3), infra/postgres implements MembershipStore.
package org
