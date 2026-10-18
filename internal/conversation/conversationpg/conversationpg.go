package conversationpg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventKinds returns conversation's routers for registration with realtime's
// reader.
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		conversation.KindPosted:        conversation.RoutePosted,
		conversation.KindMessagesMoved: conversation.RouteMoved,
	}
}

// NewChannels builds lookups on the pool and creation in a locked transaction.
// maxChannels must be positive.
func NewChannels(pool *pgxpool.Pool, maxChannels int32) *conversation.Channels {
	return conversation.NewTransactionalChannels(postgres.NewChannelStore(pool, maxChannels), newTxRunner(pool), func(tx platform.Tx) conversation.ChannelCreator {
		return postgres.ChannelCreatorIn(tx, maxChannels)
	})
}

// NewTopics builds the topic lookups on conversation's pool-bound store.
func NewTopics(pool *pgxpool.Pool) *conversation.Topics {
	return conversation.NewTopics(postgres.NewTopicStore(pool))
}

// NewPosting builds posting with org's sequence and realtime's appender
// factories bound to its transaction. A nil notifier disables notifications.
func NewPosting(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, notifier conversation.Notifier) *conversation.Posting {
	return conversation.NewPosting(newTxRunner(pool), writerIn, sequences, events, notifier)
}

// NewPagePosting binds posting and its read-state factory to the same
// transaction, and checks member references through org's directory and
// quoted messages through conversation's reads in a snapshot of their own.
func NewPagePosting(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, reads conversation.PostReadWriterIn, members conversation.MemberDirectoryIn, notifier conversation.Notifier) *conversation.Posting {
	return conversation.NewPagePosting(newTxRunner(pool), writerIn, sequences, events, reads, newSnapshotRunner(pool), members, readStoreIn, notifier)
}

// NewBrancher builds branching with org's sequence, realtime's appender and
// read-state factories bound to its transaction. A nil notifier disables notifications.
func NewBrancher(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, reads conversation.ReadRangeWriterIn, notifier conversation.Notifier) *conversation.Brancher {
	return conversation.NewBrancher(newTxRunner(pool), writerIn, sequences, events, reads, notifier)
}

// NewReader builds the page, single-message and batch use case over the pool.
// Nil counts skips count work and its local statement budget.
func NewReader(pool *pgxpool.Pool, members conversation.MemberDirectoryIn, accounts conversation.AccountDirectoryIn, cursor conversation.EventCursorIn, counts conversation.PageCountsIn) *conversation.Reader {
	// Page and sidebar counts cap candidates, but fragmented read sets and
	// above-floor filtering can scan history. Only these count snapshots need
	// longer than the pool default; explicit URL statement timeouts still win.
	return conversation.NewReader(newSnapshotRunner(pool), snapshotRunner{pool: pool, statementTimeout: time.Minute}, readStoreIn, members, accounts, cursor, counts)
}

// DefaultChannelCreatorIn returns the default-channel creator bound to setup's
// transaction. Composition roots adapt it to org.DefaultChannelCreatorIn with
// a named function (decision 26).
func DefaultChannelCreatorIn(tx platform.Tx) *postgres.DefaultChannelCreator {
	return postgres.DefaultChannelCreatorIn(tx)
}

// commitLimit matches unread's five-second limit, leaving room within the
// server's ten-second shutdown grace for a COMMIT or ROLLBACK to finish.
const commitLimit = 5 * time.Second

// newTxRunner supplies channel creation, posting, branching and pinning with
// a bounded commit independent of request cancellation; fn still follows ctx.
func newTxRunner(pool *pgxpool.Pool) conversation.TxRunner { return txRunner{pool: pool} }

type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTxDetachedCommit(ctx, r.pool, commitLimit, fn)
}

// writerIn returns posting's and branching's writes bound to the caller's
// transaction; it is a conversation.WriterIn.
func writerIn(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }

// newSnapshotRunner keeps bounded reads and posting validation on the session
// budget, without an extra statement to configure it.
func newSnapshotRunner(pool *pgxpool.Pool) conversation.SnapshotRunner {
	return snapshotRunner{pool: pool}
}

type snapshotRunner struct {
	pool             *pgxpool.Pool
	statementTimeout time.Duration
}

func (r snapshotRunner) InSnapshot(ctx context.Context, fn func(platform.Snapshot) error) error {
	return platform.InSnapshotWithStatementTimeout(ctx, r.pool, r.statementTimeout, fn)
}

// readStoreIn returns conversation's reads bound to the caller's snapshot;
// it is a conversation.ReadStoreIn.
func readStoreIn(snapshot platform.Snapshot) conversation.ReadStore {
	return postgres.ReadStoreIn(snapshot)
}

// MessageSequencesIn binds conversation's channel bounds to the caller's transaction.
func MessageSequencesIn(tx platform.Tx) conversation.MessageSequences {
	return postgres.MessageSequencesIn(tx)
}

// TopicReadCandidatesIn binds topic unread bounds to the caller's transaction.
func TopicReadCandidatesIn(tx platform.Tx) conversation.TopicReadCandidates {
	return postgres.TopicReadCandidatesIn(tx)
}

// ChannelUnreadIn binds capped channel counts to the caller's snapshot.
func ChannelUnreadIn(s platform.Snapshot) conversation.ChannelUnread {
	return postgres.ChannelUnreadIn(s)
}

// TopicUnreadIn binds topic counts to the caller's snapshot.
func TopicUnreadIn(s platform.Snapshot) conversation.TopicUnread {
	return postgres.TopicUnreadIn(s)
}

// NewPinning binds pin changes and invalidations to the same transaction.
func NewPinning(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, notifier conversation.Notifier) *conversation.Pinning {
	return conversation.NewPinning(newTxRunner(pool), writerIn, func(tx platform.Tx) conversation.PinWriter { return postgres.WriterIn(tx) }, sequences, events, notifier)
}

// PinEventKinds supplies the channel-wide pin invalidation router.
func PinEventKinds() realtime.Kinds {
	return realtime.Kinds{conversation.KindPinsChanged: conversation.RoutePinsChanged}
}
