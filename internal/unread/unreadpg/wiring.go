package unreadpg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres"
)

// NewReading builds reading with a pool-backed transaction runner.
func NewReading(pool *pgxpool.Pool, feed *unread.FeedWriter, topic *unread.TopicWriter, publisher unread.ReadPublisher) *unread.Reading {
	return unread.NewReading(txRunner{pool: pool}, feed, topic, publisher)
}

// commitLimit bounds a read's COMMIT or ROLLBACK independently of the request,
// within the server's ten-second shutdown grace.
const commitLimit = 5 * time.Second

// txRunner detaches only the commit from request cancellation, so a read the
// server committed is reported as committed and published.
type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTxDetachedCommit(ctx, r.pool, commitLimit, fn)
}

// WriterIn binds the read-range writer to the caller's transaction.
func WriterIn(tx platform.Tx) *postgres.Writer { return postgres.WriterIn(tx) }

// ChannelRangesIn binds sidebar range reads to the caller's snapshot.
func ChannelRangesIn(s platform.Snapshot) unread.ChannelRanges { return postgres.ChannelRangesIn(s) }

// TopicStateIn binds channel ranges and requested topic floors to the caller's snapshot.
func TopicStateIn(s platform.Snapshot) unread.TopicStateReader { return postgres.TopicStateIn(s) }

// NewFeedWriter builds the feed use case with injected message and cursor factories.
func NewFeedWriter(messages unread.ChannelMessagesIn, cursor unread.EventCursorIn) *unread.FeedWriter {
	return unread.NewFeedWriter(func(tx platform.Tx) unread.RangeWriter { return WriterIn(tx) }, messages, cursor)
}

// PersistedIn binds unbounded raw read-state capture to the caller's snapshot.
func PersistedIn(s platform.Snapshot) unread.PersistedReader { return postgres.PersistedIn(s) }
