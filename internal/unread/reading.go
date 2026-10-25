package unread

import (
	"context"
	"errors"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// TxRunner runs a read-state transaction owned by the use case.
type TxRunner interface {
	// InTx commits when fn returns nil and rolls back otherwise, returning
	// fn's error or a failed commit.
	InTx(context.Context, func(platform.Tx) error) error
}

// ReadPublisher receives count invalidations, independently of request cancellation.
type ReadPublisher interface {
	// ReadChanged asks the member's sidebars to recount persisted state. It
	// is a recount request, not proof that a read committed: after a COMMIT
	// with an unknown outcome it may describe a read that never committed,
	// or arrive before the commit completes on the server.
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
	r.publish(err, changed, scope)
	return err
}

// FeedShown reads one run of a resolved channel, bounded by the page's cursor.
func (r *Reading) FeedShown(ctx context.Context, scope Scope, joinedEventSeq, cursor, from, through int64) error {
	var changed bool
	err := r.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		changed, err = r.feed.ReadShown(ctx, tx, scope, joinedEventSeq, cursor, from, through)
		return err
	})
	r.publish(err, changed, scope)
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
	r.publish(err, changed, scope.Scope)
	return err
}

// TopicShown reads one run of a resolved topic, bounded by the page's cursor.
func (r *Reading) TopicShown(ctx context.Context, scope TopicScope, joinedEventSeq, cursor, from, through int64) error {
	var changed bool
	err := r.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		changed, err = r.topic.ReadShown(ctx, tx, scope, joinedEventSeq, cursor, from, through)
		return err
	})
	_ = changed
	return err
}

// publish requests a recount once per read: after a successful commit with
// changed=true, and also when that COMMIT was sent and its outcome is unknown,
// because the member's tabs recount persisted state, which is right whether or
// not the read committed, while silence could leave them stale until they
// reconnect. A rollback, a server's error reply, a COMMIT never sent and any
// failure while beginning or writing publish nothing; the error is returned
// as is, so the request still fails.
func (r *Reading) publish(err error, changed bool, scope Scope) {
	if changed && r.publisher != nil && (err == nil || errors.Is(err, platform.ErrCommitUnknown)) {
		r.publisher.ReadChanged(scope.OrganizationID, scope.MemberID)
	}
}
