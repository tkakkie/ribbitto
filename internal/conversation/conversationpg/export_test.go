package conversationpg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
)

// NewTxRunnerForTest lets external flow tests inject writers into the wiring's runner.
func NewTxRunnerForTest(pool *pgxpool.Pool) conversation.TxRunner { return newTxRunner(pool) }
