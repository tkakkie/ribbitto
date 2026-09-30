// Package message holds posting and message reads with current author names.
//
// Feature: message (feature map in docs/architecture/README.md), which owns
// the message table. Exported API: Service, the Store interface it needs and
// ErrInvalidBody, and Reader with History: Before reads a Page below an
// event_seq bound; One reads an Entry by organisation, channel and event_seq,
// returning ErrNotFound for a missing or out-of-scope message.
// Author names use member.Directory (org)
// followed by auth.Directory (identity); message queries neither feature.
// It uses org through authz.Membership; posting reports a channel
// outside the caller's organisation as channel.ErrNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// because the sequence must be taken in the writing transaction.
package message
