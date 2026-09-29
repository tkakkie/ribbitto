// Package message holds the message use cases, starting with posting.
//
// Feature: message (feature map in docs/architecture/README.md), which owns
// the message table. Exported API: Service, the Store interface it needs and
// ErrInvalidBody. It uses org through authz.Membership and reports a channel
// outside the caller's organisation as channel.ErrNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// because the sequence must be taken in the writing transaction.
package message
