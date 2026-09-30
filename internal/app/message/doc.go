// Package message holds the message use cases, posting and reading history with author names.
//
// Feature: message (feature map in docs/architecture/README.md), which owns
// the message table. Exported API: Service, the Store interface it needs and
// ErrInvalidBody, and Reader with History, which reads history a Page at a
// time below an event_seq bound, and ChannelPage, the snapshot result.
// Author names use member.Directory (org) followed by auth.Directory
// (identity); message queries neither feature.
// It uses org through authz.Membership and reports a channel
// outside the caller's organisation as channel.ErrNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// and writes realtime's event_log, so the sequence and event commit together.
package message
