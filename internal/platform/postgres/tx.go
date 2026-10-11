package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/internal/handle"
)

// Tx is an open read-write transaction that a use case can pass to the
// modules it orchestrates (decision 26). It has no SQL methods: only stores,
// through the pgxbridge package, can run queries on it.
type Tx = handle.Tx

// Snapshot is an open read-only, repeatable-read transaction: every read
// through it sees the same committed state. Like Tx, it is opaque.
type Snapshot = handle.Snapshot

// InTx runs fn in a new transaction on pool. It follows pgx.BeginFunc: a
// failure to begin or commit is returned; an error from fn rolls the
// transaction back and is returned as is; a panic in fn rolls it back and
// panics again with the same value.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return fn(handle.NewTx(tx)) })
}

// ErrCommitUnknown wraps a commit error after which the server may or may not
// have committed: COMMIT was sent and no reply arrived (a timeout or a broken
// connection). A server's error or ROLLBACK reply (a rollback) and a failure
// before COMMIT was sent (never applied) are returned unwrapped.
var ErrCommitUnknown = errors.New("commit outcome unknown")

// InTxDetachedCommit is InTx, except that once fn succeeds COMMIT runs under
// its own limit instead of ctx's cancellation and deadline. A request
// cancelled while COMMIT is in flight could otherwise get an error for a
// commit the server already finished, and a caller that acts only on success
// (Reading's post-commit publication) would skip it for good. The limit bounds
// how long the call waits for an unresponsive server: on expiry it returns an
// error wrapping ErrCommitUnknown, and pgx closes the connection, which the pool
// then discards in the background (pgx's own cleanup limits apply there). A rollback after
// an error or a panic runs under the same kind of limit. fn follows ctx.
func InTxDetachedCommit(ctx context.Context, pool *pgxpool.Pool, limit time.Duration, fn func(Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	// After a commit this is a no-op; on an error or a panic it rolls back.
	defer func() {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	if err := fn(handle.NewTx(tx)); err != nil {
		return err
	}
	commit, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	if err := tx.Commit(commit); err != nil {
		// A server's error reply or a ROLLBACK reply means the transaction
		// rolled back; a closed transaction or an error before COMMIT was
		// sent means it was never applied. Only a COMMIT sent without a reply
		// leaves the outcome unknown.
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
			return pgErr
		}
		if errors.Is(err, pgx.ErrTxCommitRollback) || errors.Is(err, pgx.ErrTxClosed) || pgconn.SafeToRetry(err) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}
	return nil
}

// InSnapshot runs fn in a new read-only, repeatable-read transaction on pool,
// with InTx's error and panic behaviour. Writes through it fail.
func InSnapshot(ctx context.Context, pool *pgxpool.Pool, fn func(Snapshot) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	return pgx.BeginTxFunc(ctx, pool, options, func(tx pgx.Tx) error { return fn(handle.NewSnapshot(tx)) })
}
