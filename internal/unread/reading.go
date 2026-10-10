package unread

import (
	"context"

	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// TxRunner runs a read-state transaction owned by the use case.
type TxRunner interface {
	// InTx commits when fn returns nil and rolls back otherwise, returning
	// fn's error or a failed commit.
	InTx(context.Context, func(platform.Tx) error) error
}

// Reading marks a feed or topic read in its own transaction.
type Reading struct {
	runner TxRunner
	feed   *FeedWriter
	topic  *TopicWriter
}

// NewReading builds reading with a runner and transaction-bound writers.
func NewReading(runner TxRunner, feed *FeedWriter, topic *TopicWriter) *Reading {
	return &Reading{runner: runner, feed: feed, topic: topic}
}

// Feed reads a resolved channel through cursor using org's persisted join sequence.
func (r *Reading) Feed(ctx context.Context, scope Scope, joinedEventSeq, cursor int64) error {
	return r.runner.InTx(ctx, func(tx platform.Tx) error {
		return r.feed.Read(ctx, tx, scope, joinedEventSeq, cursor)
	})
}

// Topic reads a resolved topic through cursor using org's persisted join sequence.
func (r *Reading) Topic(ctx context.Context, scope TopicScope, joinedEventSeq, cursor int64) error {
	return r.runner.InTx(ctx, func(tx platform.Tx) error {
		return r.topic.Read(ctx, tx, scope, joinedEventSeq, cursor)
	})
}
