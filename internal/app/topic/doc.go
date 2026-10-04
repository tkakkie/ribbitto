// Package topic holds the topic feature's API: the conversations inside a
// channel, its default topic, the topic view and branching (decision 21,
// docs/domain/topics.md).
//
// Feature: topic (feature map in docs/architecture/features.md), which owns
// the topic table. Exported API: Store, Brancher with its BranchStore,
// Branch and Notifier, MaxBranchMessages, and ErrConflict and
// ErrInvalidBranch. Every lookup is scoped by organisation and channel, so a
// topic of another channel is not found. Topic, its name rule, its errors and
// the batch label lookup (TopicDirectory) are conversation's, not this
// package's.
package topic
