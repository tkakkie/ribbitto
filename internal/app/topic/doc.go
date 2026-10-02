// Package topic holds the topic feature's API: the conversations inside a
// channel, its default topic, the topic view and later branching
// (decision 21, docs/domain/topics.md).
//
// Feature: topic (feature map in docs/architecture/features.md), which owns
// the topic table. Exported API: Store, Directory (batch label lookup), ErrNotFound,
// ErrInvalidName and ErrNameTaken. Views use its scoped reads; branching
// use cases arrive in #305. Every lookup is scoped by organisation and
// channel, so a topic of another channel is not found.
package topic
