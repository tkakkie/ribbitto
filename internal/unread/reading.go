package unread

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// TxRunner runs a read-state transaction owned by the use case.
type TxRunner interface {
	// InTx commits when fn returns nil and rolls back otherwise, returning
	// fn's error or a failed commit.
	InTx(context.Context, func(platform.Tx) error) error
}

// ReadPublisher receives effective reads after commit, independently of request cancellation.
type ReadPublisher interface {
	ReadChanged(organization, member kernel.ID)
}

// Reading marks a feed or topic read in its own transaction.
type Reading struct {
	runner    TxRunner
	feed      *FeedWriter
	topic     *TopicWriter
	publisher ReadPublisher
}

// NewReading builds reading with transaction-bound writers and an optional publisher.
func NewReading(runner TxRunner, feed *FeedWriter, topic *TopicWriter, publisher ReadPublisher) *Reading {
	return &Reading{runner: runner, feed: feed, topic: topic, publisher: publisher}
}

// Feed reads a resolved channel through cursor using org's persisted join sequence.
func (r *Reading) Feed(ctx context.Context, scope Scope, joinedEventSeq, cursor int64) error {
	var changed bool
	err := r.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		changed, err = r.feed.Read(ctx, tx, scope, joinedEventSeq, cursor)
		return err
	})
	if err == nil && changed && r.publisher != nil {
		r.publisher.ReadChanged(scope.OrganizationID, scope.MemberID)
	}
	return err
}

// Topic reads a resolved topic through cursor using org's persisted join sequence.
func (r *Reading) Topic(ctx context.Context, scope TopicScope, joinedEventSeq, cursor int64) error {
	var changed bool
	err := r.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		changed, err = r.topic.Read(ctx, tx, scope, joinedEventSeq, cursor)
		return err
	})
	if err == nil && changed && r.publisher != nil {
		r.publisher.ReadChanged(scope.OrganizationID, scope.MemberID)
	}
	return err
}
