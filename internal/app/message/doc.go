// Package message holds posting.
//
// Feature: message (feature map in docs/architecture/features.md), which owns
// the message table. Exported API: Service with New and NewWithNotifier, the
// Store interface it needs, and Notifier. Message, its body rule, its errors
// and the history reader
// (Reader, Entry, Page, ChannelPage, PageSize) are conversation's, not this
// package's.
// It uses org through org.Membership; posting reports a channel
// outside the caller's organisation as conversation.ErrChannelNotFound. Listed
// exception: posting advances organization.event_seq, which org owns,
// and writes realtime's event_log, so the sequence and event commit together.
// PostToTopic selects a scoped conversation.Topic, returning
// conversation.ErrTopicNotFound for a missing or out-of-scope topic;
// Post uses the channel default.
// NewWithNotifier accepts a Notifier (Raise) called only after Store.PostToTopic
// succeeds, meaning commit completed; New leaves notifications disabled.
package message
