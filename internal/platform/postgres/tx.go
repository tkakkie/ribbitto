package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
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

// InTxDetachedCommit is InTx, except that once fn succeeds the commit runs
// without ctx's cancellation. A request cancelled while COMMIT is in flight
// could otherwise get an error for a commit the server already finished, and
// a caller that acts only on success (Reading's post-commit publication)
// would skip it for good. fn and the rollbacks still follow ctx.
func InTxDetachedCommit(ctx context.Context, pool *pgxpool.Pool, fn func(Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	// After a commit this is a no-op; on an error or a panic it rolls back.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(handle.NewTx(tx)); err != nil {
		return err
	}
	return tx.Commit(context.WithoutCancel(ctx))
}

// InSnapshot runs fn in a new read-only, repeatable-read transaction on pool,
// with InTx's error and panic behaviour. Writes through it fail.
func InSnapshot(ctx context.Context, pool *pgxpool.Pool, fn func(Snapshot) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	return pgx.BeginTxFunc(ctx, pool, options, func(tx pgx.Tx) error { return fn(handle.NewSnapshot(tx)) })
}
