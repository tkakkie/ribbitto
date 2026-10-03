// Package topic holds the topic feature's API: the conversations inside a
// channel, its default topic, the topic view and branching (decision 21,
// docs/domain/topics.md).
//
// Feature: topic (feature map in docs/architecture/features.md), which owns
// the topic table. Exported API: Store, Directory (batch label lookup),
// Brancher with its BranchStore, Branch and Notifier, MaxBranchMessages,
// KindMessagesMoved with Moved, EncodeMoved, DecodeMoved and RouteMoved (the
// messages.moved kind, its payload and its routing), and
// ErrNotFound, ErrInvalidName, ErrNameTaken, ErrConflict and
// ErrInvalidBranch. Every lookup is scoped by organisation and channel, so
// a topic of another channel is not found.
package topic
