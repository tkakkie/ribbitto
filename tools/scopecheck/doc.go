// Package scopecheck tests organisation scoping in production SQL. It uses
// tools/internal/sqlwalk for query loading and AST traversal, and deliberately
// rejects shapes whose organisation predicates its own policy cannot prove.
package scopecheck
