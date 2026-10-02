// Package topic holds the topic feature's API: the conversations inside a
// channel, its default topic, and later the topic view and branching
// (decision 21, docs/domain/topics.md).
//
// Feature: topic (feature map in docs/architecture/README.md), which owns
// the topic table. Exported API: the Store interface and ErrNotFound,
// ErrInvalidName and ErrNameTaken. Use cases arrive with the views that
// need them (#303, #305); every lookup is scoped by organisation and
// channel, so a topic of another channel is not found.
package topic
