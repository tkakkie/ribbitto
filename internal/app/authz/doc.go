// Package authz is the single place that decides who may see an
// organisation's data. Handlers and the real-time delivery loop call it;
// they never decide access themselves.
//
// Feature: org (feature map in docs/architecture/README.md), which owns the
// organization, member and setup tables; this package only reads them.
// Exported API: Authorizer (including MayReceive, which the real-time stream
// calls for each event after rendering it, right before sending), Membership, the Store interface it needs
// and ErrNotFound. It is also part of the shared kernel: every feature checks
// access through it.
package authz
