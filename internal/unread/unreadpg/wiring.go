package unreadpg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres"
)

// NewReading builds reading with a pool-backed transaction runner.
func NewReading(pool *pgxpool.Pool, feed *unread.FeedWriter, topic *unread.TopicWriter) *unread.Reading {
	return unread.NewReading(txRunner{pool: pool}, feed, topic)
}

type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTx(ctx, r.pool, fn)
}

// WriterIn binds the read-range writer to the caller's transaction.
func WriterIn(tx platform.Tx) *postgres.Writer { return postgres.WriterIn(tx) }

// NewFeedWriter builds the feed use case with injected message and cursor factories.
func NewFeedWriter(messages unread.ChannelMessagesIn, cursor unread.EventCursorIn) *unread.FeedWriter {
	return unread.NewFeedWriter(func(tx platform.Tx) unread.RangeWriter { return WriterIn(tx) }, messages, cursor)
}
