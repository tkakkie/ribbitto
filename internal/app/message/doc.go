// Package message holds the message use cases, posting and reading history with author names.
//
// Feature: message (feature map in docs/architecture/README.md), which owns
// the message table. Exported API: Service, the Store interface it needs and
// ErrInvalidBody, and Reader with History. Author names use member.Directory
// (org) followed by auth.Directory (identity); message queries neither feature.
// It uses org through authz.Membership and reports a channel
// outside the caller's organisation as channel.ErrNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// because the sequence must be taken in the writing transaction.
package message
