// Package setup authorizes and validates installation-wide first-run setup.
//
// Feature: org (feature map in docs/architecture/features.md), which owns the
// organization, member and setup tables. Exported API: Service with Input,
// Result and ValidationErrors, the Store interface it needs, and ErrToken
// and ErrCompleted. Listed exception: setup also creates the first account,
// an identity table, in the same transaction as the organisation.
package setup
