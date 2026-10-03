package postgres

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
)

// QueryCounter counts the statements a pool runs, for the development-only
// metrics listener. It records only which kind of statement ran, never its
// SQL text or arguments, so a snapshot cannot leak data.
type QueryCounter struct {
	queries, begins, commits, rollbacks atomic.Int64
}

// QueryCounts is a QueryCounter snapshot of statements attempted. Every
// value is cumulative since the counter was created and never resets, so a
// caller compares two snapshots.
type QueryCounts struct {
	// Queries counts every statement other than the three below.
	Queries                    int64
	Begins, Commits, Rollbacks int64
}

// NewQueryCounter returns a counter to pass to OpenPool.
func NewQueryCounter() *QueryCounter { return &QueryCounter{} }

// TraceQueryStart implements pgx.QueryTracer. pgx sends BEGIN, COMMIT and
// ROLLBACK as ordinary statements, so they are told apart by their first word.
// Counting happens before PostgreSQL answers: the counts are statements
// attempted, so a COMMIT that fails still counts as a commit.
func (c *QueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Fields splits on any whitespace, so BEGIN followed by a newline or a tab
	// is still a transaction statement.
	var word string
	if fields := strings.Fields(data.SQL); len(fields) > 0 {
		word = fields[0]
	}
	switch strings.ToLower(word) {
	case "begin":
		c.begins.Add(1)
	case "commit":
		c.commits.Add(1)
	case "rollback":
		c.rollbacks.Add(1)
	default:
		c.queries.Add(1)
	}
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer; nothing is recorded at the end.
func (*QueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Counts returns the counts so far.
func (c *QueryCounter) Counts() QueryCounts {
	return QueryCounts{Queries: c.queries.Load(), Begins: c.begins.Load(), Commits: c.commits.Load(), Rollbacks: c.rollbacks.Load()}
}
