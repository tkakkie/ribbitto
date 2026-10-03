// Package message holds posting and message reads with current author names.
//
// Feature: message (feature map in docs/architecture/features.md), which owns
// the message table. Exported API: Service, the Store interface it needs and
// ErrInvalidBody, EncodePosted (the message.posted payload it publishes),
// and Reader with History: Before reads a Page below an
// event_seq bound with an optional topic filter; One reads an Entry by
// organisation, channel and event_seq,
// returning ErrNotFound for a missing or out-of-scope message; Many reads a
// bounded ID batch with the same scoping and hydration; ChannelPage is
// the page snapshot's result. topic.Directory resolves topic labels in one
// batch per page.
// Author names use member.Directory (org)
// followed by identity.Directory; message queries neither feature.
// It uses org through authz.Membership; posting reports a channel
// outside the caller's organisation as channel.ErrNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// and writes realtime's event_log, so the sequence and event commit together.
// PostToTopic selects a scoped topic; Post uses the channel default.
// NewWithNotifier accepts a Notifier (Raise) called only after Store.PostToTopic
// succeeds, meaning commit completed; New leaves notifications disabled.
package message
