package orgpg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/org"
)

// NewTxRunnerForTest lets external flow tests inject writers into the wiring's runner.
func NewTxRunnerForTest(pool *pgxpool.Pool) org.TxRunner { return newTxRunner(pool) }
