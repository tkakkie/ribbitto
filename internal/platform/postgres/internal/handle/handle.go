package handle

import "github.com/jackc/pgx/v5"

// Tx is an open read-write transaction. Its pgx transaction is unexported so
// that holding a Tx never gives SQL access by itself.
type Tx struct{ tx pgx.Tx }

// Snapshot is an open read-only repeatable-read transaction.
type Snapshot struct{ tx pgx.Tx }

// NewTx wraps tx.
func NewTx(tx pgx.Tx) Tx { return Tx{tx: tx} }

// NewSnapshot wraps tx, which must be read-only and repeatable-read.
func NewSnapshot(tx pgx.Tx) Snapshot { return Snapshot{tx: tx} }

// UnwrapTx returns the pgx transaction inside t.
func UnwrapTx(t Tx) pgx.Tx { return t.tx }

// UnwrapSnapshot returns the pgx transaction inside s.
func UnwrapSnapshot(s Snapshot) pgx.Tx { return s.tx }
