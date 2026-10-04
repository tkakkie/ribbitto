// Package signup controls installation-wide account registration.
//
// Feature: identity (feature map in docs/architecture/features.md), which
// owns the account and session tables. Exported API: Service, the Store
// interface it needs, ValidationErrors (org's type), and ErrClosed and
// ErrEmailTaken. Listed exception: sign-up also creates the member row and
// advances organization.event_seq, org's tables, in the same transaction as
// the account.
package signup
